package main

import (
        "bytes"
        "database/sql"
        "encoding/json"
        "fmt"
        "html/template"
        "log"
        "math"
        "net/http"
        "sort"
        "strconv"
        "strings"
        "sync"
        "time"

        _ "github.com/mattn/go-sqlite3"
)

// WIB = UTC+7 (Waktu Indonesia Barat)
var wib = time.FixedZone("WIB", 7*60*60)

func nowWIB() time.Time {
        return time.Now().In(wib)
}

// ── Models ────────────────────────────────────────────────────────────────────

type Result struct {
        ID        int
        Tanggal   string
        Sesi      int
        Nomor     string
        CreatedAt string
}

type D2Stat struct {
        D2          string
        AllFreq     int
        SesiFreq    int
        LastSeen    int
        Score       float64
        MarkovScore float64
        CorrScore   float64
}

type Prediction struct {
        Metode   string
        Top2D    []D2Stat
        BBFS     string
        BBFSList []string
        Alasan   string
}

type BBFSValidation struct {
        Tanggal   string
        Sesi      int
        Digits    string
        DigitList []string
        Pairs     []string
        Result    string
        D2        string
        IsHit     bool
        HasResult bool
}

type WinRate struct {
        Total int
        Hits  int
        Miss  int
        Pct   int
}

type SesiStat struct {
        Sesi  int
        Total int
        Hits  int
        Miss  int
        Pct   int
        BBFS  string
}

type PaitoCell struct {
        Nomor string
        Color string
        D2    string
}

type PaitoRow struct {
        Tanggal string
        Cells   []PaitoCell
}

type FreqItem struct {
        D2    string
        Count int
        Color string
}

type BBFSResult struct {
        Digits     string
        DigitList  []string
        Pairs2D    []string
        TotalPairs int
}

type Stats struct {
        Total int
}

type BacktestRow struct {
        Tanggal  string
        Sesi     int
        BBFS     string
        TopD2    string
        IsHit    bool
        ActualD2 string
        RunTotal int
        RunHits  int
        RunPct   int
}

type SesiPred struct {
        Sesi      int
        BBFS      string
        BBFSList  []string
        Top5      []D2Stat
        IsNext    bool
        HasResult bool
        ActualD2  string
}

type EkorCell struct {
        Count int
        Pct   int
        Hot   bool // top 3 dari ekor asal ini
}

type EkorTransRow struct {
        From  string
        Total int
        Trans [10]EkorCell
}

type EkorStatsData struct {
        LastEkor   string
        LastNomor  string
        LastTgl    string
        LastSesi   int
        Rows       []EkorTransRow
        EkorFreq   [10]int
        TotalData  int
}

type PageData struct {
        Results           []Result
        Predictions       []Prediction
        PaitoRows         []PaitoRow
        FreqItems         []FreqItem
        BBFS              *BBFSResult
        BBFS2             *BBFSResult
        Error             string
        Message           string
        CurrentDate       string
        CurrentSesi       int
        NextSesi          int
        LastResultDate    string
        LastResultSesi    int
        NextPredDate      string
        Stats             *Stats
        Validations       []BBFSValidation
        WR                WinRate
        SesiStats         []SesiStat
        CurrentPrediction *Prediction
        CompStats         []CompStatRow
        LearnedWeights    LearnedWeights
        BacktestRows      []BacktestRow
        SesiPreds         []SesiPred
        PredDate          string
        EkorStats         *EkorStatsData
}

// ── DB ────────────────────────────────────────────────────────────────────────

var db *sql.DB

func initDB() {
        var err error
        db, err = sql.Open("sqlite3", "./toto.db")
        if err != nil {
                log.Fatal(err)
        }
        db.Exec(`DROP TABLE IF EXISTS tune_history`)
        db.Exec(`DROP TABLE IF EXISTS bbfs_sessions`)
        db.Exec(`CREATE TABLE IF NOT EXISTS results (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                tanggal TEXT NOT NULL,
                sesi INTEGER NOT NULL,
                nomor TEXT NOT NULL,
                created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
                UNIQUE(tanggal, sesi)
        )`)
        db.Exec(`CREATE TABLE IF NOT EXISTS bbfs_preds (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                tanggal TEXT NOT NULL,
                sesi INTEGER NOT NULL,
                digits TEXT NOT NULL,
                source TEXT DEFAULT 'AI-LOKAL',
                created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
                UNIQUE(tanggal, sesi, source)
        )`)
        // Tabel komponen prediksi — simpan skor komponen top-1 D2 saat prediksi dibuat
        db.Exec(`CREATE TABLE IF NOT EXISTS pred_components (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                tanggal TEXT NOT NULL,
                sesi INTEGER NOT NULL,
                top_d2 TEXT NOT NULL,
                bbfs TEXT NOT NULL,
                markov_score REAL DEFAULT 0,
                gap_score REAL DEFAULT 0,
                dow_score REAL DEFAULT 0,
                corr_score REAL DEFAULT 0,
                total_score REAL DEFAULT 0,
                is_hit INTEGER DEFAULT -1,
                actual_d2 TEXT DEFAULT '',
                created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
                UNIQUE(tanggal, sesi)
        )`)
        // Tabel bobot yang dipelajari — 1 baris, di-update setelah setiap eval
        db.Exec(`CREATE TABLE IF NOT EXISTS component_weights (
                id INTEGER PRIMARY KEY,
                markov_mult REAL DEFAULT 1.0,
                gap_mult REAL DEFAULT 1.0,
                dow_mult REAL DEFAULT 1.0,
                corr_mult REAL DEFAULT 1.0,
                eval_count INTEGER DEFAULT 0,
                updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
        )`)
        db.Exec(`INSERT OR IGNORE INTO component_weights (id,markov_mult,gap_mult,dow_mult,corr_mult,eval_count) VALUES (1,1.0,1.0,1.0,1.0,0)`)
}

// ── Learned Weights ───────────────────────────────────────────────────────────

type LearnedWeights struct {
        MarkovMult float64
        CorrMult   float64
        EvalCount  int
}

type CompStatRow struct {
        Component string
        HitAvg    float64
        MissAvg   float64
        Ratio     float64
        Mult      float64
        Active    bool // cukup data untuk tuning
}

var globalWeights = LearnedWeights{MarkovMult: 1.0, CorrMult: 1.0}
var globalWeightsMu sync.RWMutex

func loadLearnedWeights() LearnedWeights {
        var lw LearnedWeights
        lw.MarkovMult = 1.0
        lw.CorrMult = 1.0
        db.QueryRow(`SELECT markov_mult,corr_mult,eval_count FROM component_weights WHERE id=1`).
                Scan(&lw.MarkovMult, &lw.CorrMult, &lw.EvalCount)
        return lw
}

// retuneWeights menghitung ulang bobot dari eval history dan menyimpannya
func retuneWeights() {
        type avg struct{ hit, miss, hitN, missN float64 }
        stats := map[string]*avg{
                "markov": {},
                "corr":   {},
        }
        rows, err := db.Query(`SELECT markov_score,corr_score,is_hit FROM pred_components WHERE is_hit >= 0`)
        if err != nil {
                return
        }
        defer rows.Close()
        total := 0
        for rows.Next() {
                var m, c float64
                var hit int
                rows.Scan(&m, &c, &hit)
                total++
                vals := map[string]float64{"markov": m, "corr": c}
                for k, v := range vals {
                        if hit == 1 {
                                stats[k].hit += v
                                stats[k].hitN++
                        } else {
                                stats[k].miss += v
                                stats[k].missN++
                        }
                }
        }
        if total < 10 {
                return // tunggu sampai ada cukup data
        }
        calcMult := func(key string, cur float64) float64 {
                s := stats[key]
                hitAvg := 0.0
                if s.hitN > 0 {
                        hitAvg = s.hit / s.hitN
                }
                missAvg := 0.0
                if s.missN > 0 {
                        missAvg = s.miss / s.missN
                }
                denom := math.Max(missAvg, 0.01)
                ratio := hitAvg / denom
                raw := math.Max(0.4, math.Min(ratio, 2.5))
                return math.Round((0.7*cur+0.3*raw)*100) / 100
        }
        cur := loadLearnedWeights()
        newM := calcMult("markov", cur.MarkovMult)
        newC := calcMult("corr", cur.CorrMult)
        db.Exec(`UPDATE component_weights SET markov_mult=?,corr_mult=?,eval_count=?,updated_at=CURRENT_TIMESTAMP WHERE id=1`,
                newM, newC, total)
        globalWeightsMu.Lock()
        globalWeights = LearnedWeights{MarkovMult: newM, CorrMult: newC, EvalCount: total}
        globalWeightsMu.Unlock()
        log.Printf("🎓 Auto-tune: markov×%.2f corr×%.2f (%d eval)", newM, newC, total)
}

// savePredComponent simpan skor komponen top-1 D2 saat prediksi dibuat
func savePredComponent(tanggal string, sesi int, stats []D2Stat, bbfs string) {
        if len(stats) == 0 {
                return
        }
        top := stats[0]
        db.Exec(`INSERT OR REPLACE INTO pred_components
                (tanggal,sesi,top_d2,bbfs,markov_score,corr_score,total_score,is_hit,actual_d2)
                VALUES (?,?,?,?,?,?,?,-1,'')`,
                tanggal, sesi, top.D2, bbfs,
                top.MarkovScore, top.CorrScore, top.Score)
}

// evalPrediction: saat result masuk, tandai prediksi sesi itu HIT/MISS
func evalPrediction(tanggal string, sesi int, actualNomor string) {
        if len(actualNomor) < 4 {
                return
        }
        actualD2 := actualNomor[2:4]
        // Cek apakah ada prediksi yang tersimpan untuk sesi ini
        var digits string
        err := db.QueryRow(`SELECT digits FROM bbfs_preds WHERE tanggal=? AND sesi=?`, tanggal, sesi).Scan(&digits)
        if err != nil || len(digits) < 5 {
                return
        }
        hit := 0
        if isHit(digits, actualD2) {
                hit = 1
        }
        // Tandai pred_components (jika ada)
        db.Exec(`UPDATE pred_components SET is_hit=?,actual_d2=? WHERE tanggal=? AND sesi=? AND is_hit=-1`,
                hit, actualD2, tanggal, sesi)
        // Trigger retune setiap 5 eval baru
        var evalCount int
        db.QueryRow(`SELECT eval_count FROM component_weights WHERE id=1`).Scan(&evalCount)
        var totalEval int
        db.QueryRow(`SELECT COUNT(*) FROM pred_components WHERE is_hit >= 0`).Scan(&totalEval)
        if totalEval >= 10 && totalEval%5 == 0 {
                go retuneWeights()
        }
}

// getCompStats mengambil statistik efektivitas komponen untuk stats page
func getCompStats() []CompStatRow {
        type avg struct{ hit, miss, hitN, missN float64 }
        stats := map[string]*avg{
                "Markov": {}, "Corr": {},
        }
        rows, err := db.Query(`SELECT markov_score,corr_score,is_hit FROM pred_components WHERE is_hit >= 0`)
        if err != nil {
                return nil
        }
        defer rows.Close()
        total := 0
        for rows.Next() {
                var m, c float64
                var hit int
                rows.Scan(&m, &c, &hit)
                total++
                if hit == 1 {
                        stats["Markov"].hit += m; stats["Markov"].hitN++
                        stats["Corr"].hit += c; stats["Corr"].hitN++
                } else {
                        stats["Markov"].miss += m; stats["Markov"].missN++
                        stats["Corr"].miss += c; stats["Corr"].missN++
                }
        }
        lw := loadLearnedWeights()
        mults := map[string]float64{"Markov": lw.MarkovMult, "Corr": lw.CorrMult}
        order := []string{"Markov", "Corr"}
        var result []CompStatRow
        for _, name := range order {
                s := stats[name]
                hitAvg, missAvg := 0.0, 0.0
                if s.hitN > 0 {
                        hitAvg = s.hit / s.hitN
                }
                if s.missN > 0 {
                        missAvg = s.miss / s.missN
                }
                ratio := 0.0
                if missAvg > 0 {
                        ratio = hitAvg / missAvg
                }
                result = append(result, CompStatRow{
                        Component: name,
                        HitAvg:    math.Round(hitAvg*100) / 100,
                        MissAvg:   math.Round(missAvg*100) / 100,
                        Ratio:     math.Round(ratio*100) / 100,
                        Mult:      mults[name],
                        Active:    total >= 20,
                })
        }
        return result
}

// ── DB helpers ────────────────────────────────────────────────────────────────

func getResults(limit int) []Result {
        rows, err := db.Query(
                `SELECT id, tanggal, sesi, nomor, COALESCE(created_at,'')
                 FROM results ORDER BY tanggal DESC, sesi DESC LIMIT ?`, limit)
        if err != nil {
                return nil
        }
        defer rows.Close()
        var res []Result
        for rows.Next() {
                var r Result
                rows.Scan(&r.ID, &r.Tanggal, &r.Sesi, &r.Nomor, &r.CreatedAt)
                res = append(res, r)
        }
        return res
}

func calcStats() *Stats {
        var total int
        db.QueryRow(`SELECT COUNT(*) FROM results`).Scan(&total)
        return &Stats{Total: total}
}

// getLastResultEntry ambil result terakhir yang diinput ke DB
func getLastResultEntry() (tanggal string, sesi int) {
        db.QueryRow(
                `SELECT tanggal, sesi FROM results ORDER BY tanggal DESC, sesi DESC LIMIT 1`,
        ).Scan(&tanggal, &sesi)
        return
}

// nextSessionAfter hitung sesi berikutnya setelah sesi yang diberikan
func nextSessionAfter(tanggal string, sesi int) (nextDate string, nextSesi int) {
        nextSesi = sesi + 1
        nextDate = tanggal
        if nextSesi > 6 {
                nextSesi = 1
                t, err := time.Parse("2006-01-02", tanggal)
                if err == nil {
                        nextDate = t.AddDate(0, 0, 1).Format("2006-01-02")
                }
        }
        return
}

// ── BBFS helpers ──────────────────────────────────────────────────────────────

func generateBBFS(input string) *BBFSResult {
        seen := map[string]bool{}
        var digits []string
        for _, c := range input {
                s := string(c)
                if c >= '0' && c <= '9' && !seen[s] {
                        seen[s] = true
                        digits = append(digits, s)
                }
                if len(digits) == 6 {
                        break
                }
        }
        if len(digits) < 5 {
                return nil
        }
        n := len(digits)
        var pairs []string
        for i := 0; i < n; i++ {
                for j := 0; j < n; j++ {
                        if i != j {
                                pairs = append(pairs, digits[i]+digits[j])
                        }
                }
        }
        return &BBFSResult{
                Digits:     strings.Join(digits, ""),
                DigitList:  digits,
                Pairs2D:    pairs,
                TotalPairs: len(pairs),
        }
}

func isAllDigits(s string) bool {
        for _, c := range s {
                if c < '0' || c > '9' {
                        return false
                }
        }
        return len(s) > 0
}

func isHit(digits, d2 string) bool {
        if len(d2) < 2 || len(digits) < 5 {
                return false
        }
        res := generateBBFS(digits)
        if res == nil {
                return false
        }
        for _, p := range res.Pairs2D {
                if p == d2 {
                        return true
                }
        }
        return false
}

// ── Prediksi & Validasi ───────────────────────────────────────────────────────

// savePrediction menyimpan prediksi — TIDAK menimpa yang sudah ada (INSERT OR IGNORE)
// Pakai saveOrUpdatePrediction kalau ingin paksa update
func savePrediction(tanggal string, sesi int, digits, source string) {
        if len(digits) < 5 {
                return
        }
        db.Exec(
                `INSERT OR IGNORE INTO bbfs_preds (tanggal, sesi, digits, source) VALUES (?,?,?,?)`,
                tanggal, sesi, digits, source)
}

// saveOrUpdatePrediction menimpa prediksi lama (hanya untuk autoPredict sesi berikutnya)
func saveOrUpdatePrediction(tanggal string, sesi int, digits, source string) {
        if len(digits) < 5 {
                return
        }
        db.Exec(
                `INSERT OR REPLACE INTO bbfs_preds (tanggal, sesi, digits, source) VALUES (?,?,?,?)`,
                tanggal, sesi, digits, source)
}

func autoPredict(tanggal string, sesi int) string {
        nextDate, nextSesi := nextSessionAfter(tanggal, sesi)

        stats := analyzeD2Enhanced(nextSesi)
        bbfs := buildBBFSFromStats(stats)
        if len(bbfs) < 5 {
                return ""
        }

        saveOrUpdatePrediction(nextDate, nextSesi, bbfs, "AI-LOKAL")
        savePredComponent(nextDate, nextSesi, stats, bbfs)

        return fmt.Sprintf("Auto-prediksi Sesi %d (%s): BBFS %s", nextSesi, nextDate, bbfs)
}

func getBBFSValidations(limit int) []BBFSValidation {
        rows, err := db.Query(
                `SELECT tanggal, sesi, digits FROM bbfs_preds
                 ORDER BY tanggal DESC, sesi DESC LIMIT ?`, limit)
        if err != nil {
                return nil
        }
        defer rows.Close()

        var list []BBFSValidation
        for rows.Next() {
                var v BBFSValidation
                rows.Scan(&v.Tanggal, &v.Sesi, &v.Digits)

                res := generateBBFS(v.Digits)
                if res != nil {
                        v.Pairs = res.Pairs2D
                        v.DigitList = res.DigitList
                }

                var nomor string
                err := db.QueryRow(
                        `SELECT nomor FROM results WHERE tanggal=? AND sesi=?`,
                        v.Tanggal, v.Sesi).Scan(&nomor)
                if err == nil && len(nomor) >= 4 {
                        v.HasResult = true
                        v.Result = nomor
                        v.D2 = nomor[2:4]
                        v.IsHit = isHit(v.Digits, v.D2)
                }

                list = append(list, v)
        }
        return list
}

func calcWinRate() WinRate {
        rows, err := db.Query(`
                SELECT bp.tanggal, bp.sesi, bp.digits
                FROM bbfs_preds bp
                INNER JOIN results r ON r.tanggal=bp.tanggal AND r.sesi=bp.sesi
                WHERE bp.source='AI-LOKAL'`)
        if err != nil {
                return WinRate{}
        }
        defer rows.Close()

        var total, hits int
        for rows.Next() {
                var tanggal, digits string
                var sesi int
                rows.Scan(&tanggal, &sesi, &digits)

                var nomor string
                db.QueryRow(`SELECT nomor FROM results WHERE tanggal=? AND sesi=?`, tanggal, sesi).Scan(&nomor)
                if len(nomor) < 4 {
                        continue
                }
                total++
                d2 := nomor[2:4]
                if isHit(digits, d2) {
                        hits++
                }
        }

        pct := 0
        if total > 0 {
                pct = hits * 100 / total
        }
        return WinRate{Total: total, Hits: hits, Miss: total - hits, Pct: pct}
}

// calcSesiStats hitung akurasi per sesi 1-6
// calcSesiWeight mengembalikan bobot dinamis untuk sesiFreq berdasarkan akurasi
// historis sesi tersebut. Semakin tinggi win rate, semakin besar bobot diberikan.
func calcSesiWeight(targetSesi int) float64 {
        stats := calcSesiStats()
        for _, s := range stats {
                if s.Sesi != targetSesi {
                        continue
                }
                if s.Total < 5 {
                        return 6.0 // data terlalu sedikit, gunakan bobot default
                }
                switch {
                case s.Pct >= 45:
                        return 9.0 // sesi sangat akurat — boost tinggi
                case s.Pct >= 35:
                        return 7.5
                case s.Pct >= 25:
                        return 6.0 // bobot default
                case s.Pct >= 15:
                        return 4.5
                default:
                        return 3.0 // sesi kurang akurat — kurangi pengaruh sesiFreq
                }
        }
        return 6.0
}

func calcSesiStats() []SesiStat {
        var result []SesiStat
        for s := 1; s <= 6; s++ {
                rows, err := db.Query(`
                        SELECT bp.digits, r.nomor
                        FROM bbfs_preds bp
                        JOIN results r ON r.tanggal = bp.tanggal AND r.sesi = bp.sesi
                        WHERE bp.sesi = ?`, s)
                total, hits := 0, 0
                if err == nil {
                        for rows.Next() {
                                var digits, nomor string
                                rows.Scan(&digits, &nomor)
                                if len(nomor) >= 4 {
                                        total++
                                        if isHit(digits, nomor[2:4]) {
                                                hits++
                                        }
                                }
                        }
                        rows.Close()
                }
                pct := 0
                if total > 0 {
                        pct = hits * 100 / total
                }
                var latestBBFS string
                db.QueryRow(
                        `SELECT digits FROM bbfs_preds WHERE sesi=? ORDER BY tanggal DESC, id DESC LIMIT 1`, s,
                ).Scan(&latestBBFS)

                result = append(result, SesiStat{
                        Sesi: s, Total: total, Hits: hits, Miss: total - hits, Pct: pct, BBFS: latestBBFS,
                })
        }
        return result
}

// ── Paito ─────────────────────────────────────────────────────────────────────

func buildPaito(days int) []PaitoRow {
        rows, err := db.Query(
                `SELECT DISTINCT tanggal FROM results ORDER BY tanggal DESC LIMIT ?`, days)
        if err != nil {
                return nil
        }
        var dates []string
        for rows.Next() {
                var d string
                rows.Scan(&d)
                dates = append(dates, d)
        }
        rows.Close()

        var paito []PaitoRow
        for _, date := range dates {
                row := PaitoRow{Tanggal: date}
                for sesi := 1; sesi <= 6; sesi++ {
                        var nomor string
                        err := db.QueryRow(
                                `SELECT nomor FROM results WHERE tanggal=? AND sesi=?`, date, sesi).Scan(&nomor)
                        cell := PaitoCell{}
                        if err == nil {
                                cell.Nomor = nomor
                                cell.Color = d2Color(nomor)
                                if len(nomor) >= 4 {
                                        cell.D2 = nomor[2:]
                                }
                        } else {
                                cell.Nomor = "----"
                                cell.Color = "#2d3748"
                        }
                        row.Cells = append(row.Cells, cell)
                }
                paito = append(paito, row)
        }
        return paito
}

func d2Color(nomor string) string {
        if len(nomor) < 2 {
                return "#2d3748"
        }
        d2 := nomor[len(nomor)-2:]
        n, err := strconv.Atoi(d2)
        if err != nil {
                return "#2d3748"
        }
        palette := []string{
                "#e53e3e", "#dd6b20", "#d69e2e", "#38a169", "#319795",
                "#3182ce", "#553c9a", "#b83280", "#2b6cb0", "#276749",
        }
        return palette[(n/10)%10]
}

// ── AI Lokal ──────────────────────────────────────────────────────────────────

type rawEntry struct {
        tanggal string
        sesi    int
        nomor   string
}

func getAllResults() []rawEntry {
        rows, err := db.Query(
                `SELECT tanggal, sesi, nomor FROM results ORDER BY tanggal ASC, sesi ASC`)
        if err != nil {
                return nil
        }
        defer rows.Close()
        var all []rawEntry
        for rows.Next() {
                var e rawEntry
                rows.Scan(&e.tanggal, &e.sesi, &e.nomor)
                all = append(all, e)
        }
        return all
}

func markovTransition(entries []rawEntry) map[string]map[string]float64 {
        trans := map[string]map[string]float64{}
        for i := 1; i < len(entries); i++ {
                if len(entries[i-1].nomor) < 4 || len(entries[i].nomor) < 4 {
                        continue
                }
                from := entries[i-1].nomor[2:4]
                to := entries[i].nomor[2:4]
                if trans[from] == nil {
                        trans[from] = map[string]float64{}
                }
                trans[from][to]++
        }
        for from, toMap := range trans {
                total := 0.0
                for _, c := range toMap {
                        total += c
                }
                if total > 0 {
                        for to := range toMap {
                                trans[from][to] /= total
                        }
                }
        }
        return trans
}

func markovSesiTransition(entries []rawEntry, fromSesi, toSesi int) map[string]float64 {
        counts := map[string]float64{}
        total := 0.0
        for i := 0; i < len(entries)-1; i++ {
                cur := entries[i]
                nxt := entries[i+1]
                if !(cur.sesi == fromSesi && nxt.sesi == toSesi) {
                        continue
                }
                // Validasi: harus hari sama (sesi dalam sehari) atau lintas tengah malam (S6→S1)
                if cur.tanggal != nxt.tanggal {
                        // S6→S1 lintas hari diperbolehkan
                        if !(fromSesi == 6 && toSesi == 1) {
                                continue
                        }
                }
                if len(nxt.nomor) < 4 {
                        continue
                }
                d2 := nxt.nomor[2:4]
                counts[d2]++
                total++
        }
        nextProb := map[string]float64{}
        if total > 0 {
                for d2, c := range counts {
                        nextProb[d2] = c / total
                }
        }
        return nextProb
}

func crossSesiPattern(entries []rawEntry, targetSesi int) map[string]float64 {
        scores := map[string]float64{}
        byDate := map[string]map[int]string{}
        for _, e := range entries {
                if byDate[e.tanggal] == nil {
                        byDate[e.tanggal] = map[int]string{}
                }
                byDate[e.tanggal][e.sesi] = e.nomor
        }
        for _, sesiMap := range byDate {
                target, ok := sesiMap[targetSesi]
                if !ok || len(target) < 4 {
                        continue
                }
                targetD2 := target[2:4]
                for s := 1; s < targetSesi; s++ {
                        nomor, ok := sesiMap[s]
                        if !ok || len(nomor) < 4 {
                                continue
                        }
                        if nomor[2:4] == targetD2 {
                                gap := targetSesi - s
                                scores[targetD2] += 1.0 / float64(gap)
                        }
                }
        }
        return scores
}

func periodikPattern(entries []rawEntry, targetSesi int) map[string]float64 {
        sesiOnly := []string{}
        for _, e := range entries {
                if e.sesi == targetSesi && len(e.nomor) >= 4 {
                        sesiOnly = append(sesiOnly, e.nomor[2:4])
                }
        }
        scores := map[string]float64{}
        n := len(sesiOnly)
        if n < 6 {
                return scores
        }
        for _, period := range []int{3, 4, 5, 6, 7} {
                if n < period*2 {
                        continue
                }
                for i := period; i < n; i++ {
                        if sesiOnly[i] == sesiOnly[i-period] {
                                distFromEnd := n - 1 - i
                                if distFromEnd < period {
                                        bonus := math.Max(0, float64(period-distFromEnd)) / float64(period)
                                        scores[sesiOnly[i]] += bonus * 2.0
                                }
                        }
                }
        }
        return scores
}

// streakBonus menghitung berapa kali d2 muncul berturut-turut di ujung list sesi
func streakBonus(sesiOnly []string, d2 string) float64 {
        streak := 0
        for i := len(sesiOnly) - 1; i >= 0; i-- {
                if sesiOnly[i] == d2 {
                        streak++
                } else {
                        break
                }
        }
        if streak >= 3 {
                return float64(streak) * 4.0 // hot streak — naik terus
        }
        if streak == 2 {
                return 2.0
        }
        return 0
}

// ── Upgrade 1: Sesi-Specific Markov (sesi N hari ini → sesi N besok) ──────────
// Berbeda dengan markovSesiTransition (S4→S5), ini melacak S5→S5 lintas hari.
func markovSesiSelf(entries []rawEntry, sesi int) map[string]map[string]float64 {
        sesiSeq := []string{}
        for _, e := range entries {
                if e.sesi == sesi && len(e.nomor) >= 4 {
                        sesiSeq = append(sesiSeq, e.nomor[2:4])
                }
        }
        trans := map[string]map[string]float64{}
        for i := 1; i < len(sesiSeq); i++ {
                from, to := sesiSeq[i-1], sesiSeq[i]
                if trans[from] == nil {
                        trans[from] = map[string]float64{}
                }
                trans[from][to]++
        }
        for from, toMap := range trans {
                total := 0.0
                for _, c := range toMap {
                        total += c
                }
                if total > 0 {
                        for to := range toMap {
                                trans[from][to] /= total
                        }
                }
        }
        return trans
}

// ── Upgrade 2: Digit Pair Correlation (korelasi puluhan→satuan) ───────────────
// Jika digit puluhan dari result sesi sebelumnya adalah X, 2D mana yang
// paling sering muncul di sesi target? Mendeteksi pola posisional tersembunyi.
func digitPairCorrelation(entries []rawEntry, targetSesi int) map[string]float64 {
        scores := map[string]float64{}
        prevSesi := targetSesi - 1
        if prevSesi < 1 {
                prevSesi = 6
        }
        // crossDay: prevSesi > targetSesi artinya prevSesi ada di hari kalender sebelumnya
        // Contoh: targetSesi=1 (00:01), prevSesi=6 (23:00 hari sebelumnya)
        crossDay := prevSesi > targetSesi

        // Pasangkan hasil prevSesi dan targetSesi per tanggal
        byDate := map[string]map[int]string{}
        for _, e := range entries {
                if byDate[e.tanggal] == nil {
                        byDate[e.tanggal] = map[int]string{}
                }
                byDate[e.tanggal][e.sesi] = e.nomor
        }
        tensFreq := map[string]map[string]float64{}
        for date, sesiMap := range byDate {
                curr := sesiMap[targetSesi]
                if len(curr) < 4 {
                        continue
                }
                var prev string
                if crossDay {
                        // prevSesi ada di hari sebelumnya
                        t, err := time.Parse("2006-01-02", date)
                        if err != nil {
                                continue
                        }
                        prevDate := t.AddDate(0, 0, -1).Format("2006-01-02")
                        if m, ok := byDate[prevDate]; ok {
                                prev = m[prevSesi]
                        }
                } else {
                        prev = sesiMap[prevSesi]
                }
                if len(prev) < 4 {
                        continue
                }
                tens := string(prev[2]) // digit puluhan result sesi sebelumnya
                d2 := curr[2:4]
                if tensFreq[tens] == nil {
                        tensFreq[tens] = map[string]float64{}
                }
                tensFreq[tens][d2]++
        }
        // Cari digit puluhan dari result prevSesi terakhir
        lastPrev := ""
        for i := len(entries) - 1; i >= 0; i-- {
                if entries[i].sesi == prevSesi && len(entries[i].nomor) >= 4 {
                        lastPrev = entries[i].nomor
                        break
                }
        }
        if len(lastPrev) < 4 {
                return scores
        }
        tens := string(lastPrev[2])
        if distMap, ok := tensFreq[tens]; ok {
                total := 0.0
                for _, c := range distMap {
                        total += c
                }
                if total > 0 {
                        for d2, c := range distMap {
                                scores[d2] = c / total
                        }
                }
        }
        return scores
}

// ── Upgrade 3: Rolling Window Adaptif ────────────────────────────────────────
// Pilih window (hari) yang menghasilkan distribusi paling terkonsentrasi
// (coefficient of variation tertinggi) untuk sesi target.
func adaptiveWindow(entries []rawEntry, targetSesi int, now time.Time) int {
        windows := []int{14, 21, 30, 45, 60}
        bestWindow := 45
        bestCV := 0.0
        for _, w := range windows {
                cutoff := now.AddDate(0, 0, -w)
                freq := map[string]int{}
                total := 0
                for _, e := range entries {
                        if e.sesi != targetSesi || len(e.nomor) < 4 {
                                continue
                        }
                        t, err := time.Parse("2006-01-02", e.tanggal)
                        if err != nil || t.Before(cutoff) {
                                continue
                        }
                        freq[e.nomor[2:4]]++
                        total++
                }
                if total < 5 || len(freq) == 0 {
                        continue
                }
                mean := float64(total) / float64(len(freq))
                variance := 0.0
                for _, c := range freq {
                        diff := float64(c) - mean
                        variance += diff * diff
                }
                variance /= float64(len(freq))
                cv := math.Sqrt(variance) / mean
                if cv > bestCV {
                        bestCV = cv
                        bestWindow = w
                }
        }
        return bestWindow
}


// abCorrelation: 2 digit depan (AB) sesi sebelumnya → CD apa yang sering muncul di targetSesi
func abCorrelation(entries []rawEntry, targetSesi int) map[string]float64 {
        now := nowWIB()
        prevSesi := targetSesi - 1
        if prevSesi < 1 {
                prevSesi = 6
        }
        // Cari AB terakhir dari sesi sebelumnya
        lastAB := ""
        for i := len(entries) - 1; i >= 0; i-- {
                if entries[i].sesi == prevSesi && len(entries[i].nomor) >= 4 {
                        lastAB = entries[i].nomor[0:2]
                        break
                }
        }
        if lastAB == "" {
                return nil
        }
        scores := map[string]float64{}
        total := 0.0
        // Hitung: saat prevSesi punya AB=lastAB, CD apa yang muncul di targetSesi hari itu?
        for _, pe := range entries {
                if pe.sesi != prevSesi || len(pe.nomor) < 4 || pe.nomor[0:2] != lastAB {
                        continue
                }
                peDate, err := time.Parse("2006-01-02", pe.tanggal)
                if err != nil {
                        continue
                }
                for _, ne := range entries {
                        if ne.sesi != targetSesi || len(ne.nomor) < 4 {
                                continue
                        }
                        neDate, err2 := time.Parse("2006-01-02", ne.tanggal)
                        if err2 != nil {
                                continue
                        }
                        diff := neDate.Sub(peDate).Hours() / 24
                        if diff < 0 || diff > 1 {
                                continue
                        }
                        age := now.Sub(peDate).Hours() / 24
                        w := math.Exp(-age/35.0)*2.5 + 0.2
                        scores[ne.nomor[2:4]] += w
                        total += w
                        break
                }
        }
        if total < 0.1 {
                return nil
        }
        for d2 := range scores {
                scores[d2] /= total
        }
        return scores
}

// calcEkorTransitionBoost membangun matriks transisi ekor dari histori Macau sendiri.
// Mencari ekor terakhir sebelum sesi target, lalu mengembalikan skor boost per digit ekor
// berdasarkan seberapa sering digit itu muncul setelah ekor sebelumnya.
func calcEkorTransitionBoost(entries []rawEntry, targetSesi int) map[string]float64 {
        if len(entries) < 10 {
                return nil
        }

        // Salin slice agar tidak merusak urutan di caller
        sorted := make([]rawEntry, len(entries))
        copy(sorted, entries)
        sort.Slice(sorted, func(i, j int) bool {
                if sorted[i].tanggal == sorted[j].tanggal {
                        return sorted[i].sesi < sorted[j].sesi
                }
                return sorted[i].tanggal < sorted[j].tanggal
        })
        entries = sorted

        // Bangun matriks transisi ekor → ekor berikutnya (semua sesi berurutan)
        type ekorPair struct{ from, to string }
        trans := map[ekorPair]int{}
        fromCount := map[string]int{}
        for i := 0; i < len(entries)-1; i++ {
                if len(entries[i].nomor) < 4 || len(entries[i+1].nomor) < 4 {
                        continue
                }
                from := string(entries[i].nomor[3])
                to := string(entries[i+1].nomor[3])
                trans[ekorPair{from, to}]++
                fromCount[from]++
        }

        // Cari ekor terakhir yang diketahui (result sebelum sesi target)
        lastDate := entries[len(entries)-1].tanggal
        lastEkor := ""
        for i := len(entries) - 1; i >= 0; i-- {
                e := entries[i]
                if len(e.nomor) < 4 {
                        continue
                }
                // Ambil result terakhir sebelum sesi target (sesi lebih awal atau hari sebelumnya)
                if e.sesi < targetSesi || e.tanggal < lastDate {
                        lastEkor = string(e.nomor[3])
                        break
                }
        }
        if lastEkor == "" {
                // Fallback: ekor dari result terakhir apapun
                for i := len(entries) - 1; i >= 0; i-- {
                        if len(entries[i].nomor) >= 4 {
                                lastEkor = string(entries[i].nomor[3])
                                break
                        }
                }
        }
        if lastEkor == "" || fromCount[lastEkor] == 0 {
                return nil
        }

        // Hitung probabilitas transisi dari lastEkor → setiap ekor berikutnya
        boost := map[string]float64{}
        total := float64(fromCount[lastEkor])
        for d := 0; d <= 9; d++ {
                ds := strconv.Itoa(d)
                count := trans[ekorPair{lastEkor, ds}]
                if count > 0 {
                        prob := float64(count) / total
                        boost[ds] = prob // contoh: ekor "7" dapat 0.30 jika 30% transisi ke sana
                }
        }
        return boost
}

func analyzeD2Enhanced(targetSesi int) []D2Stat {
        now := nowWIB()
        allEntries := getAllResults()
        if len(allEntries) == 0 {
                return nil
        }

        // ── Upgrade 3: Rolling Window Adaptif ────────────────────────────────────
        // Pilih window yang menghasilkan distribusi paling terkonsentrasi.
        days := adaptiveWindow(allEntries, targetSesi, now)
        cutoff := now.AddDate(0, 0, -days)
        var filtered []rawEntry
        for _, e := range allEntries {
                t, err := time.Parse("2006-01-02", e.tanggal)
                if err != nil || !t.Before(cutoff) {
                        filtered = append(filtered, e)
                }
        }
        allEntries = filtered
        if len(allEntries) == 0 {
                return nil
        }

        // Bobot sesiFreq dinamis dari akurasi historis sesi ini (auto-learning)
        sesiWeight := calcSesiWeight(targetSesi)

        type stat struct {
                allFreq     float64
                sesiFreq    float64
                lastIdx     int
                lastSesiIdx int
                markov      float64
                streak      float64
                corr        float64
                gap         float64 // boost untuk 2D yang lama tidak muncul di sesi ini
                ekorBoost   float64 // boost dari transisi ekor historis Macau
        }
        stats := map[string]*stat{}
        ensure := func(d2 string) {
                if stats[d2] == nil {
                        stats[d2] = &stat{lastIdx: 9999, lastSesiIdx: 9999}
                }
        }

        // Frekuensi semua sesi — super-boost 3 hari terakhir
        for i, e := range allEntries {
                if len(e.nomor) < 4 {
                        continue
                }
                d2 := e.nomor[2:4]
                ensure(d2)
                t, err := time.Parse("2006-01-02", e.tanggal)
                weight := 0.2
                if err == nil {
                        age := now.Sub(t).Hours() / 24
                        switch {
                        case age <= 3:
                                weight = 4.0 - age*0.2 // 4.0 → 3.4 (boost terbaru moderat)
                        case age <= 7:
                                weight = 3.0 - age*0.1 // 3.0 → 2.3
                        case age <= 30:
                                weight = math.Exp(-age/18.0)*2.5 + 0.3
                        default:
                                weight = math.Exp(-age/50.0)*1.2 + 0.2
                        }
                }
                stats[d2].allFreq += weight
                if stats[d2].lastIdx == 9999 {
                        stats[d2].lastIdx = len(allEntries) - i
                }
        }

        // Frekuensi sesi spesifik — decay lebih tajam untuk sesi
        sesiOnly := []string{}
        sesiEntries := []rawEntry{}
        for _, e := range allEntries {
                if e.sesi == targetSesi {
                        sesiEntries = append(sesiEntries, e)
                        if len(e.nomor) >= 4 {
                                sesiOnly = append(sesiOnly, e.nomor[2:4])
                        }
                }
        }
        for i, e := range sesiEntries {
                if len(e.nomor) < 4 {
                        continue
                }
                d2 := e.nomor[2:4]
                ensure(d2)
                t, err := time.Parse("2006-01-02", e.tanggal)
                weight := 0.4
                if err == nil {
                        age := now.Sub(t).Hours() / 24
                        switch {
                        case age <= 3:
                                weight = 6.0 - age*0.3 // 6.0 → 5.1 (boost sesi terbaru moderat)
                        case age <= 7:
                                weight = 4.5 - age*0.2 // 4.5 → 3.1
                        case age <= 21:
                                weight = math.Exp(-age/12.0)*3.5 + 0.4
                        default:
                                weight = math.Exp(-age/35.0)*2.0 + 0.2
                        }
                }
                stats[d2].sesiFreq += weight
                if stats[d2].lastSesiIdx == 9999 {
                        stats[d2].lastSesiIdx = len(sesiEntries) - i
                }
        }

        // ── Gap/Cooldown Boost: 2D yang lama tidak muncul di sesi ini ────────────
        // Hanya berlaku untuk pair yang sudah pernah muncul secara global (allFreq > 0.5)
        // agar pair yang memang jarang/tidak relevan tidak mendapat boost palsu
        for _, s := range stats {
                if s.allFreq < 0.5 {
                        s.gap = 0 // pair terlalu jarang secara global → skip
                        continue
                }
                if s.lastSesiIdx == 9999 {
                        s.gap = 2.0 // belum pernah muncul di sesi ini tapi aktif secara global
                        continue
                }
                switch {
                case s.lastSesiIdx > 25:
                        s.gap = 3.0
                case s.lastSesiIdx > 18:
                        s.gap = 2.0
                case s.lastSesiIdx > 12:
                        s.gap = 1.2
                case s.lastSesiIdx > 8:
                        s.gap = 0.5
                default:
                        s.gap = 0
                }
        }

        // ── Streak Analysis ───────────────────────────────────────────────────────
        for _, d2 := range sesiOnly {
                ensure(d2)
                stats[d2].streak = streakBonus(sesiOnly, d2)
        }

        // ── Markov: umum + sesi-to-sesi + self + lintas sesi + periodik ──────────
        prevSesi := targetSesi - 1
        if prevSesi < 1 {
                prevSesi = 6
        }

        // Markov umum (semua sesi, dari result prevSesi terakhir)
        markovGen := markovTransition(allEntries)
        lastPrevSesiD2 := ""
        for i := len(allEntries) - 1; i >= 0; i-- {
                if allEntries[i].sesi == prevSesi && len(allEntries[i].nomor) >= 4 {
                        lastPrevSesiD2 = allEntries[i].nomor[2:4]
                        break
                }
        }
        if lastPrevSesiD2 != "" {
                if nextProbs, ok := markovGen[lastPrevSesiD2]; ok {
                        for d2, prob := range nextProbs {
                                ensure(d2)
                                stats[d2].markov += prob * 5.0
                        }
                }
        }

        // Markov sesi N-1 → sesi N (pola antar-sesi dalam sehari)
        for d2, prob := range markovSesiTransition(allEntries, prevSesi, targetSesi) {
                ensure(d2)
                stats[d2].markov += prob * 8.0
        }

        // ── Upgrade 1: Sesi-Self Markov (sesi N → sesi N hari berikutnya) ────────
        selfMarkov := markovSesiSelf(allEntries, targetSesi)
        lastSelfD2 := ""
        for i := len(allEntries) - 1; i >= 0; i-- {
                if allEntries[i].sesi == targetSesi && len(allEntries[i].nomor) >= 4 {
                        lastSelfD2 = allEntries[i].nomor[2:4]
                        break
                }
        }
        if lastSelfD2 != "" {
                if nextProbs, ok := selfMarkov[lastSelfD2]; ok {
                        for d2, prob := range nextProbs {
                                ensure(d2)
                                stats[d2].markov += prob * 7.0 // bobot tinggi: pola sesi yg sama
                        }
                }
        }

        // Pola lintas sesi
        for d2, score := range crossSesiPattern(allEntries, targetSesi) {
                ensure(d2)
                stats[d2].markov += score * 2.5
        }

        // Pola periodik
        for d2, score := range periodikPattern(allEntries, targetSesi) {
                ensure(d2)
                stats[d2].markov += score * 3.5
        }

        // ── Upgrade 2: Digit Pair Correlation ────────────────────────────────────
        for d2, score := range digitPairCorrelation(allEntries, targetSesi) {
                ensure(d2)
                stats[d2].corr = score
        }

        // ── Upgrade 7: AB Correlation — CD setelah pola AB sesi sebelumnya ────────
        for d2, score := range abCorrelation(allEntries, targetSesi) {
                ensure(d2)
                stats[d2].corr += score * 3.5
        }

        // ── Upgrade 8: Ekor Transition Boost — dari matriks transisi Macau sendiri ─
        // Boost D2 pair yang digit ekor-nya (digit ke-2) cocok dengan prediksi transisi
        ekorTrans := calcEkorTransitionBoost(allEntries, targetSesi)
        for d2, s := range stats {
                if len(d2) != 2 {
                        continue
                }
                ekor := string(d2[1]) // digit ke-2 dari pair = ekor
                if prob, ok := ekorTrans[ekor]; ok {
                        s.ekorBoost = prob * 8.0 // probabilitas 30% → boost 2.4, probabilitas 20% → boost 1.6
                }
        }

        // ── Gabungkan semua komponen ke skor akhir ────────────────────────────────
        var list []D2Stat
        for d2, s := range stats {
                if s.sesiFreq == 0 && s.allFreq == 0 && s.markov < 0.5 && s.corr < 0.05 {
                        continue
                }
                // Formula: sesiFreq (auto-bobot) + allFreq + markov + streak
                //          + gap (overdue) + ekorBoost (transisi ekor) + corr (korelasi posisi)
                // Setiap komponen dikalikan bobot yang dipelajari dari backtesting
                globalWeightsMu.RLock()
                gw := globalWeights
                globalWeightsMu.RUnlock()
                score := s.sesiFreq*sesiWeight +
                        s.allFreq*1.5 +
                        s.markov*gw.MarkovMult +
                        s.streak +
                        s.corr*5.5*gw.CorrMult +
                        s.gap*1.2 +
                        s.ekorBoost
                lastSeen := s.lastIdx
                if s.lastSesiIdx < lastSeen {
                        lastSeen = s.lastSesiIdx
                }
                list = append(list, D2Stat{
                        D2:          d2,
                        AllFreq:     int(s.allFreq + 0.5),
                        SesiFreq:    int(s.sesiFreq + 0.5),
                        LastSeen:    lastSeen,
                        Score:       score,
                        MarkovScore: s.markov,
                        CorrScore:   s.corr,
                })
        }

        // ── (2) Filter 2D lemah: SesiFreq==0 dan AllFreq di bawah rata-rata ──────
        totalAllFreq := 0
        for _, d := range list {
                totalAllFreq += d.AllFreq
        }
        avgAllFreq := 0
        if len(list) > 0 {
                avgAllFreq = totalAllFreq / len(list)
        }
        var strong []D2Stat
        for _, d := range list {
                if d.SesiFreq == 0 && d.AllFreq < avgAllFreq {
                        continue // buang 2D lemah
                }
                strong = append(strong, d)
        }
        list = strong

        sort.Slice(list, func(i, j int) bool {
                if list[i].Score != list[j].Score {
                        return list[i].Score > list[j].Score
                }
                return list[i].D2 < list[j].D2
        })
        return list
}

func analyzeFreqItems(limit int) []FreqItem {
        rows, err := db.Query(
                `SELECT nomor FROM results ORDER BY tanggal DESC, sesi DESC LIMIT ?`, limit)
        if err != nil {
                return nil
        }
        defer rows.Close()
        freq := map[string]int{}
        for rows.Next() {
                var n string
                rows.Scan(&n)
                if len(n) >= 4 {
                        freq[n[2:4]]++
                }
        }
        type kv struct{ k string; v int }
        var sorted []kv
        for k, v := range freq {
                sorted = append(sorted, kv{k, v})
        }
        sort.Slice(sorted, func(i, j int) bool { return sorted[i].v > sorted[j].v })
        var items []FreqItem
        for _, item := range sorted {
                n, _ := strconv.Atoi(item.k)
                palette := []string{
                        "#e53e3e", "#dd6b20", "#d69e2e", "#38a169", "#319795",
                        "#3182ce", "#553c9a", "#b83280", "#2b6cb0", "#276749",
                }
                items = append(items, FreqItem{
                        D2:    item.k,
                        Count: item.v,
                        Color: palette[(n/10)%10],
                })
        }
        return items
}

// buildBBFSOptimized mencari 5 digit terbaik dari C(10,5)=252 kombinasi
// calcDigitHitRate hitung hit-rate per digit (0-9) dari riwayat BBFS + result.
// Digit yang sering masuk BBFS tapi jarang HIT mendapat penalty < 1.0.
func calcDigitHitRate() map[string]float64 {
        rows, err := db.Query(`
                SELECT bp.digits, r.nomor
                FROM bbfs_preds bp
                INNER JOIN results r ON r.tanggal=bp.tanggal AND r.sesi=bp.sesi
                WHERE bp.source='AI-LOKAL' AND LENGTH(bp.digits)>=6
                ORDER BY bp.id DESC LIMIT 80`)
        if err != nil {
                return nil
        }
        defer rows.Close()

        type digitStat struct{ inBBFS, hit int }
        ds := map[string]*digitStat{}
        for d := 0; d <= 9; d++ {
                ds[strconv.Itoa(d)] = &digitStat{}
        }

        for rows.Next() {
                var digits, nomor string
                rows.Scan(&digits, &nomor)
                if len(nomor) < 4 || len(digits) < 6 {
                        continue
                }
                actualD2 := nomor[2:4]
                hit := isHit(digits, actualD2)
                for _, ch := range digits {
                        d := string(ch)
                        if d >= "0" && d <= "9" {
                                ds[d].inBBFS++
                                if hit {
                                        ds[d].hit++
                                }
                        }
                }
        }

        mult := map[string]float64{}
        for d, s := range ds {
                if s.inBBFS < 8 {
                        mult[d] = 1.0 // data terlalu sedikit, tidak dipenalize
                        continue
                }
                rate := float64(s.hit) / float64(s.inBBFS)
                switch {
                case rate < 0.15:
                        mult[d] = 0.50 // sangat jarang hit → penalti keras
                case rate < 0.25:
                        mult[d] = 0.70 // jarang hit → penalti sedang
                case rate < 0.35:
                        mult[d] = 0.85 // sedikit di bawah rata-rata
                default:
                        mult[d] = 1.0 // normal atau bagus
                }
        }
        return mult
}

// buildBBFSFromStats mencari 6 digit terbaik dari C(10,6)=210 kombinasi.
// Pilih kombinasi yang memaksimalkan total skor D2 pair yang terbentuk
func buildBBFSFromStats(stats []D2Stat) string {
        if len(stats) == 0 {
                return ""
        }

        // Buat map skor per D2 pair
        pairScore := map[string]float64{}
        for _, s := range stats {
                pairScore[s.D2] = s.Score
        }

        // ── (4) Identifikasi 2D lemah dan digit yang berasal dari mereka ──────────
        // "Lemah" = SesiFreq==0 dan skor di bawah 40% rata-rata skor seluruh D2
        totalScore := 0.0
        for _, s := range stats {
                totalScore += s.Score
        }
        weakThreshold := 0.0
        if len(stats) > 0 {
                weakThreshold = (totalScore / float64(len(stats))) * 0.4
        }
        // Untuk setiap digit, hitung berapa banyak pair lemah vs kuat yang melibatkannya
        digitWeakCount := map[string]int{}
        digitStrongCount := map[string]int{}
        for _, s := range stats {
                if len(s.D2) != 2 {
                        continue
                }
                d0, d1 := string(s.D2[0]), string(s.D2[1])
                if s.SesiFreq == 0 && s.Score < weakThreshold {
                        digitWeakCount[d0]++
                        digitWeakCount[d1]++
                } else {
                        digitStrongCount[d0]++
                        digitStrongCount[d1]++
                }
        }

        // Hitung skor kontribusi per digit (sum dari semua pair yang melibatkan digit ini)
        digitContrib := map[string]float64{}
        for pair, score := range pairScore {
                if len(pair) == 2 {
                        digitContrib[string(pair[0])] += score
                        digitContrib[string(pair[1])] += score
                }
        }

        // Terapkan penalti 0.6x pada digit yang mayoritas pair-nya lemah
        for d := range digitContrib {
                weak := digitWeakCount[d]
                strong := digitStrongCount[d]
                if weak > strong {
                        digitContrib[d] *= 0.6
                }
        }

        // Digit hit-rate penalty dinonaktifkan — butuh 80+ data historis dulu
        // hitMult := calcDigitHitRate() — aktifkan setelah data cukup

        allDigits := []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9"}

        // Exhaustive search C(10,6) = 210 kombinasi — coverage 30 pasangan per set
        bestScore := -1.0
        bestCombo := []string{}

        for i := 0; i < 10; i++ {
                for j := i + 1; j < 10; j++ {
                        for k := j + 1; k < 10; k++ {
                                for l := k + 1; l < 10; l++ {
                                        for m := l + 1; m < 10; m++ {
                                                for n := m + 1; n < 10; n++ {
                                                        combo := []string{
                                                                allDigits[i], allDigits[j], allDigits[k],
                                                                allDigits[l], allDigits[m], allDigits[n],
                                                        }
                                                        // Hitung total skor semua 30 pasangan D2 dari 6 digit ini
                                                        score := 0.0
                                                        for _, d1 := range combo {
                                                                for _, d2 := range combo {
                                                                        if d1 != d2 {
                                                                                score += pairScore[d1+d2]
                                                                        }
                                                                }
                                                        }
                                                        if score > bestScore {
                                                                bestScore = score
                                                                bestCombo = combo
                                                        }
                                                }
                                        }
                                }
                        }
                }
        }

        if len(bestCombo) == 0 {
                // Fallback: ambil 6 digit dengan kontribusi tertinggi
                type kv struct {
                        d string
                        v float64
                }
                var sorted []kv
                for d, v := range digitContrib {
                        sorted = append(sorted, kv{d, v})
                }
                sort.Slice(sorted, func(i, j int) bool { return sorted[i].v > sorted[j].v })
                for _, kv := range sorted {
                        bestCombo = append(bestCombo, kv.d)
                        if len(bestCombo) == 6 {
                                break
                        }
                }
        }

        // Urutkan digit dalam BBFS berdasarkan kontribusi skor (digit terkuat duluan)
        sort.Slice(bestCombo, func(i, j int) bool {
                return digitContrib[bestCombo[i]] > digitContrib[bestCombo[j]]
        })

        return strings.Join(bestCombo, "")
}

// buildBBFS2FromStats: BBFS kedua dengan penalti pair yang sudah tercakup BBFS-A
// Memastikan BBFS-B punya digit berbeda untuk coverage lebih luas
func buildBBFS2FromStats(stats []D2Stat, firstDigits string) string {
        if len(stats) == 0 {
                return ""
        }
        // Buat pairScore dengan penalti pair yang kedua digitnya sudah ada di BBFS-A
        pairScore := map[string]float64{}
        for _, s := range stats {
                score := s.Score
                inFirst0 := strings.ContainsRune(firstDigits, rune(s.D2[0]))
                inFirst1 := strings.ContainsRune(firstDigits, rune(s.D2[1]))
                if inFirst0 && inFirst1 {
                        score *= 0.1 // sangat kecilkan — sudah tercakup BBFS-A
                } else if inFirst0 || inFirst1 {
                        score *= 0.6 // sedikit penalti — setengah sudah tercakup
                }
                pairScore[s.D2] = score
        }

        digitContrib := map[string]float64{}
        for pair, score := range pairScore {
                if len(pair) == 2 {
                        digitContrib[string(pair[0])] += score
                        digitContrib[string(pair[1])] += score
                }
        }

        allDigits := []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9"}
        bestScore := -1.0
        bestCombo := []string{}

        for i := 0; i < 10; i++ {
                for j := i + 1; j < 10; j++ {
                        for k := j + 1; k < 10; k++ {
                                for l := k + 1; l < 10; l++ {
                                        for m := l + 1; m < 10; m++ {
                                                for n := m + 1; n < 10; n++ {
                                                        combo := []string{
                                                                allDigits[i], allDigits[j], allDigits[k],
                                                                allDigits[l], allDigits[m], allDigits[n],
                                                        }
                                                        score := 0.0
                                                        for _, d1 := range combo {
                                                                for _, d2 := range combo {
                                                                        if d1 != d2 {
                                                                                score += pairScore[d1+d2]
                                                                        }
                                                                }
                                                        }
                                                        if score > bestScore {
                                                                bestScore = score
                                                                bestCombo = combo
                                                        }
                                                }
                                        }
                                }
                        }
                }
        }

        if len(bestCombo) == 0 {
                type kv struct {
                        d string
                        v float64
                }
                var sorted []kv
                for d, v := range digitContrib {
                        sorted = append(sorted, kv{d, v})
                }
                sort.Slice(sorted, func(i, j int) bool { return sorted[i].v > sorted[j].v })
                for _, kv := range sorted {
                        bestCombo = append(bestCombo, kv.d)
                        if len(bestCombo) == 6 {
                                break
                        }
                }
        }

        sort.Slice(bestCombo, func(i, j int) bool {
                return digitContrib[bestCombo[i]] > digitContrib[bestCombo[j]]
        })
        return strings.Join(bestCombo, "")
}

func buildAlasan(stats []D2Stat, sesi int) string {
        if len(stats) == 0 {
                return "Belum cukup data historis"
        }
        top := stats[0]
        var parts []string
        parts = append(parts, fmt.Sprintf(
                "Sesi %d: 2D terkuat %s (skor %.0f, sesi %dx, markov %.1f)",
                sesi, top.D2, top.Score, top.SesiFreq, top.MarkovScore,
        ))
        var due []string
        for _, s := range stats {
                if s.LastSeen > 12 && s.SesiFreq >= 1 {
                        due = append(due, fmt.Sprintf("%s(%d putaran)", s.D2, s.LastSeen))
                }
                if len(due) >= 3 {
                        break
                }
        }
        if len(due) > 0 {
                parts = append(parts, "Due: "+strings.Join(due, ", "))
        }
        return strings.Join(parts, ". ")
}

// ── Template ──────────────────────────────────────────────────────────────────

var tmpls map[string]*template.Template

func newFuncMap() template.FuncMap {
        return template.FuncMap{
                "string": func(r rune) string { return string(r) },
                "add":    func(a, b int) int { return a + b },
                "seq": func(n int) []int {
                        s := make([]int, n)
                        for i := range s {
                                s[i] = i + 1
                        }
                        return s
                },
                "join":    strings.Join,
                "colorOf": d2Color,
                "inBBFS": func(digits string, d2 string) bool {
                        if len(d2) != 2 || len(digits) < 5 || d2[0] == d2[1] {
                                return false
                        }
                        return strings.ContainsRune(digits, rune(d2[0])) &&
                                strings.ContainsRune(digits, rune(d2[1]))
                },
                "hasPair": func(pairs []string, d2 string) bool {
                        for _, p := range pairs {
                                if p == d2 {
                                        return true
                                }
                        }
                        return false
                },
                "formatDate": func(s string) string {
                        t, err := time.Parse("2006-01-02", s)
                        if err != nil {
                                return s
                        }
                        days := []string{"Min", "Sen", "Sel", "Rab", "Kam", "Jum", "Sab"}
                        return days[t.Weekday()] + " " + t.Format("02/01")
                },
                "prevDate": func(s string) string {
                        t, err := time.Parse("2006-01-02", s)
                        if err != nil {
                                return s
                        }
                        return t.AddDate(0, 0, -1).Format("2006-01-02")
                },
                "pct": func(count, max int) int {
                        if max == 0 {
                                return 0
                        }
                        v := count * 100 / max
                        if v < 4 {
                                return 4
                        }
                        return v
                },
                "hitClass": func(v BBFSValidation) string {
                        if !v.HasResult {
                                return "val-pending"
                        }
                        if v.IsHit {
                                return "val-hit"
                        }
                        return "val-miss"
                },
                "hitLabel": func(v BBFSValidation) string {
                        if !v.HasResult {
                                return "⏳"
                        }
                        if v.IsHit {
                                return "✅ HIT"
                        }
                        return "❌ MISS"
                },
                "pctClass": func(pct int) string {
                        if pct >= 50 {
                                return "wr-good"
                        }
                        if pct >= 30 {
                                return "wr-med"
                        }
                        return "wr-bad"
                },
        }
}

func loadTemplates() {
        pages := []string{"index", "input", "paito", "predict", "stats", "ekor-stats", "backtest"}
        tmpls = make(map[string]*template.Template, len(pages))
        for _, p := range pages {
                t, err := template.New("").Funcs(newFuncMap()).ParseFiles(
                        "templates/base.html",
                        "templates/"+p+".html",
                )
                if err != nil {
                        log.Fatalf("template %s error: %v", p, err)
                }
                tmpls[p] = t
        }
}

func render(w http.ResponseWriter, page string, data interface{}) {
        t, ok := tmpls[page]
        if !ok {
                http.Error(w, "halaman tidak ditemukan", 404)
                return
        }
        var buf bytes.Buffer
        if err := t.ExecuteTemplate(&buf, page+".html", data); err != nil {
                log.Println("render error:", err)
                http.Error(w, "Kesalahan rendering halaman: "+err.Error(), 500)
                return
        }
        w.Header().Set("Content-Type", "text/html; charset=utf-8")
        buf.WriteTo(w)
}

func recovery(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
                defer func() {
                        if rec := recover(); rec != nil {
                                log.Printf("PANIC: %v", rec)
                                http.Error(w, "Kesalahan internal, coba lagi.", 500)
                        }
                }()
                next.ServeHTTP(w, r)
        })
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// currentSesi mengembalikan sesi yang SEDANG DITUNGGU (upcoming), bukan yang sudah selesai.
// Jadwal result: S1=00:01  S2=13:00  S3=16:00  S4=19:00  S5=22:00  S6=23:00
func currentSesi() int {
        h := nowWIB().Hour()
        switch {
        case h >= 0 && h < 13:
                return 2 // sesi 1 sudah keluar (00:01), menunggu sesi 2 (13:00)
        case h >= 13 && h < 16:
                return 3 // menunggu sesi 3 (16:00)
        case h >= 16 && h < 19:
                return 4 // menunggu sesi 4 (19:00)
        case h >= 19 && h < 22:
                return 5 // menunggu sesi 5 (22:00)
        case h >= 22 && h < 23:
                return 6 // menunggu sesi 6 (23:00)
        default:
                return 1 // setelah 23:00, menunggu sesi 1 besok
        }
}

// ── Prediction helper ─────────────────────────────────────────────────────────

// getPredictionForDate ambil atau generate prediksi untuk tanggal+sesi tertentu
func getPredictionForDate(date string, sesi int) *Prediction {
        var digits string
        db.QueryRow(
                `SELECT digits FROM bbfs_preds WHERE tanggal=? AND sesi=? AND source='AI-LOKAL'`,
                date, sesi).Scan(&digits)

        stats := analyzeD2Enhanced(sesi)

        if len(digits) < 5 {
                digits = buildBBFSFromStats(stats)
                if len(digits) >= 5 {
                        savePrediction(date, sesi, digits, "AI-LOKAL")
                }
        }
        if len(digits) < 5 {
                return nil
        }

        top10 := stats
        if len(top10) > 10 {
                top10 = top10[:10]
        }

        return &Prediction{
                Metode:   "AI-LOKAL",
                Top2D:    top10,
                BBFS:     digits,
                BBFSList: strings.Split(digits, ""),
                Alasan:   buildAlasan(top10, sesi),
        }
}

// ── Handlers ──────────────────────────────────────────────────────────────────

func indexHandler(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/" {
                http.NotFound(w, r)
                return
        }

        // Prediksi berdasarkan result terakhir di DB (bukan jam)
        // Sehingga tidak kacau meski input terlambat
        lastDate, lastSesi := getLastResultEntry()
        nextDate, nextSesi := nextSessionAfter(lastDate, lastSesi)

        var pred *Prediction
        if lastDate != "" {
                // Koreksi: jika nextSesi dari DB sudah berlalu, langsung pakai
                // sesi yang sedang ditunggu sekarang (currentSesi = upcoming sesi).
                // Contoh: last DB = sesi 3, jam = 20:00 → upcoming=5, bukan 4.
                nowStr := nowWIB().Format("2006-01-02")
                if nextDate == nowStr && nextSesi < currentSesi() {
                        nextSesi = currentSesi()
                }
                pred = getPredictionForDate(nextDate, nextSesi)
        } else {
                // Belum ada data, fallback langsung ke sesi yang sedang ditunggu
                nextSesi = currentSesi()
                nextDate = nowWIB().Format("2006-01-02")
                pred = getPredictionForDate(nextDate, nextSesi)
        }

        render(w, "index", PageData{
                CurrentDate:       nowWIB().Format("2006-01-02"),
                CurrentSesi:       currentSesi(),
                NextSesi:          nextSesi,
                NextPredDate:      nextDate,
                LastResultDate:    lastDate,
                LastResultSesi:    lastSesi,
                Stats:             calcStats(),
                Message:           r.URL.Query().Get("msg"),
                Validations:       getBBFSValidations(10),
                WR:                calcWinRate(),
                CurrentPrediction: pred,
        })
}

func inputHandler(w http.ResponseWriter, r *http.Request) {
        today := nowWIB().Format("2006-01-02")
        if r.Method == "GET" {
                render(w, "input", PageData{CurrentDate: today, Results: getResults(30)})
                return
        }

        tanggal := r.FormValue("tanggal")
        sesi, _ := strconv.Atoi(r.FormValue("sesi"))
        nomor := strings.TrimSpace(r.FormValue("nomor"))

        if len(nomor) != 4 || !isAllDigits(nomor) {
                render(w, "input", PageData{Error: "Nomor harus tepat 4 angka (0-9)!", CurrentDate: tanggal, Results: getResults(30)})
                return
        }
        if _, err := db.Exec(
                `INSERT OR REPLACE INTO results (tanggal, sesi, nomor) VALUES (?,?,?)`,
                tanggal, sesi, nomor); err != nil {
                render(w, "input", PageData{Error: "Gagal simpan: " + err.Error(), CurrentDate: tanggal, Results: getResults(30)})
                return
        }

        // Evaluasi prediksi yang ada untuk sesi ini (SEBELUM generate prediksi baru)
        evalPrediction(tanggal, sesi, nomor)

        autopMsg := autoPredict(tanggal, sesi)
        msg := fmt.Sprintf("Result %s (Sesi %d) tersimpan. %s", nomor, sesi, autopMsg)
        http.Redirect(w, r, "/?msg="+strings.ReplaceAll(msg, " ", "+"), http.StatusSeeOther)
}

func inputBatchHandler(w http.ResponseWriter, r *http.Request) {
        if r.Method != "POST" {
                http.Redirect(w, r, "/input", http.StatusSeeOther)
                return
        }
        ok, fail := 0, 0
        var lastTanggal string
        var lastSesi int
        for _, line := range strings.Split(r.FormValue("batch"), "\n") {
                line = strings.TrimSpace(line)
                if line == "" {
                        continue
                }
                parts := strings.Split(line, ",")
                if len(parts) < 3 {
                        fail++
                        continue
                }
                tanggal := strings.TrimSpace(parts[0])
                sesi, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
                nomor := strings.TrimSpace(parts[2])
                if len(nomor) != 4 || !isAllDigits(nomor) || sesi < 1 || sesi > 6 {
                        fail++
                        continue
                }
                if _, terr := time.Parse("2006-01-02", tanggal); terr != nil {
                        fail++
                        continue
                }
                if _, err := db.Exec(
                        `INSERT OR REPLACE INTO results (tanggal, sesi, nomor) VALUES (?,?,?)`,
                        tanggal, sesi, nomor); err != nil {
                        fail++
                } else {
                        evalPrediction(tanggal, sesi, nomor)
                        ok++
                        lastTanggal = tanggal
                        lastSesi = sesi
                }
        }
        if lastTanggal != "" {
                autoPredict(lastTanggal, lastSesi)
        }
        http.Redirect(w, r,
                fmt.Sprintf("/?msg=Batch+selesai:+%d+sukses,+%d+gagal.", ok, fail),
                http.StatusSeeOther)
}

func paitoHandler(w http.ResponseWriter, r *http.Request) {
        days, _ := strconv.Atoi(r.URL.Query().Get("days"))
        if days <= 0 {
                days = 30
        }
        freqs := analyzeFreqItems(100)
        maxCnt := 0
        if len(freqs) > 0 {
                maxCnt = freqs[0].Count
        }
        render(w, "paito", PageData{
                PaitoRows: buildPaito(days),
                FreqItems: freqs,
                Stats:     &Stats{Total: maxCnt},
        })
}

func predictHandler(w http.ResponseWriter, r *http.Request) {
        lastDate, lastSesi := getLastResultEntry()
        today := nowWIB().Format("2006-01-02")
        next := currentSesi()

        if lastDate != "" {
                d, s := nextSessionAfter(lastDate, lastSesi)
                today = d
                next = s
                nowStr := nowWIB().Format("2006-01-02")
                if today == nowStr && next < currentSesi() {
                        next = currentSesi()
                }
        }

        // Override tanggal via query param
        if qt := r.URL.Query().Get("tanggal"); qt != "" {
                today = qt
        }

        // Ambil result yang sudah ada untuk tanggal ini
        existingResults := map[int]string{}
        rows, _ := db.Query(`SELECT sesi, nomor FROM results WHERE tanggal=?`, today)
        if rows != nil {
                for rows.Next() {
                        var s int
                        var n string
                        rows.Scan(&s, &n)
                        if len(n) >= 2 {
                                existingResults[s] = n[len(n)-2:]
                        }
                }
                rows.Close()
        }

        // Build prediksi untuk semua 6 sesi
        var sesiPreds []SesiPred
        for sesi := 1; sesi <= 6; sesi++ {
                stats := analyzeD2Enhanced(sesi)

                // Ambil BBFS dari DB atau generate
                var bbfsDigits string
                db.QueryRow(
                        `SELECT digits FROM bbfs_preds WHERE tanggal=? AND sesi=? AND source='AI-LOKAL' ORDER BY id DESC LIMIT 1`,
                        today, sesi,
                ).Scan(&bbfsDigits)
                if len(bbfsDigits) < 6 {
                        bbfsDigits = buildBBFSFromStats(stats)
                        if len(bbfsDigits) >= 6 {
                                savePrediction(today, sesi, bbfsDigits, "AI-LOKAL")
                        }
                }

                top5 := stats
                if len(top5) > 5 {
                        top5 = top5[:5]
                }

                actualD2 := existingResults[sesi]
                sp := SesiPred{
                        Sesi:      sesi,
                        BBFS:      bbfsDigits,
                        BBFSList:  strings.Split(bbfsDigits, ""),
                        Top5:      top5,
                        IsNext:    sesi == next,
                        HasResult: actualD2 != "",
                        ActualD2:  actualD2,
                }
                sesiPreds = append(sesiPreds, sp)
        }

        render(w, "predict", PageData{
                CurrentDate: today,
                NextSesi:    next,
                PredDate:    today,
                SesiPreds:   sesiPreds,
        })
}

func backtestHandler(w http.ResponseWriter, r *http.Request) {
        // Pre-compute top 2D per sesi SEBELUM membuka query utama (hindari konflik koneksi SQLite)
        topD2BySesi := map[int]string{}
        for s := 1; s <= 6; s++ {
                st := analyzeD2Enhanced(s)
                if len(st) > 0 {
                        topD2BySesi[s] = st[0].D2
                }
        }

        // Ambil semua prediksi AI-LOKAL yang sudah ada result-nya, join langsung
        rows, err := db.Query(`
                SELECT bp.tanggal, bp.sesi, bp.digits, r.nomor
                FROM bbfs_preds bp
                INNER JOIN results r ON r.tanggal = bp.tanggal AND r.sesi = bp.sesi
                WHERE bp.source = 'AI-LOKAL'
                ORDER BY bp.tanggal ASC, bp.sesi ASC`)
        if err != nil {
                http.Error(w, "DB error: "+err.Error(), 500)
                return
        }
        defer rows.Close()

        var list []BacktestRow
        total, hits := 0, 0
        for rows.Next() {
                var tanggal, digits, nomor string
                var sesi int
                rows.Scan(&tanggal, &sesi, &digits, &nomor)

                if len(nomor) < 4 || len(digits) < 5 {
                        continue
                }
                actualD2 := nomor[2:4]
                hit := isHit(digits, actualD2)

                total++
                if hit {
                        hits++
                }
                list = append(list, BacktestRow{
                        Tanggal:  tanggal,
                        Sesi:     sesi,
                        BBFS:     digits,
                        TopD2:    topD2BySesi[sesi],
                        IsHit:    hit,
                        ActualD2: actualD2,
                        RunTotal: total,
                        RunHits:  hits,
                        RunPct:   hits * 100 / total,
                })
        }

        // Balik urutan: terbaru di atas
        for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
                list[i], list[j] = list[j], list[i]
        }

        wr := WinRate{Total: total, Hits: hits, Miss: total - hits}
        if total > 0 {
                wr.Pct = hits * 100 / total
        }
        render(w, "backtest", PageData{WR: wr, BacktestRows: list})
}

func statsHandler(w http.ResponseWriter, r *http.Request) {
        render(w, "stats", PageData{
                SesiStats:      calcSesiStats(),
                WR:             calcWinRate(),
                CompStats:      getCompStats(),
                LearnedWeights: loadLearnedWeights(),
        })
}

// calcEkorStats membangun matriks transisi ekor 10×10 dari seluruh histori hasil
func calcEkorStats() *EkorStatsData {
        entries := getAllResults()
        if len(entries) < 5 {
                return nil
        }
        // Sudah urut ASC dari getAllResults
        var ekorFreq [10]int
        trans := [10][10]int{}

        for _, e := range entries {
                if len(e.nomor) < 4 {
                        continue
                }
                ek := int(e.nomor[3] - '0')
                if ek >= 0 && ek <= 9 {
                        ekorFreq[ek]++
                }
        }

        for i := 0; i < len(entries)-1; i++ {
                if len(entries[i].nomor) < 4 || len(entries[i+1].nomor) < 4 {
                        continue
                }
                from := int(entries[i].nomor[3] - '0')
                to := int(entries[i+1].nomor[3] - '0')
                if from >= 0 && from <= 9 && to >= 0 && to <= 9 {
                        trans[from][to]++
                }
        }

        var rows []EkorTransRow
        for f := 0; f <= 9; f++ {
                total := 0
                for t := 0; t <= 9; t++ {
                        total += trans[f][t]
                }
                row := EkorTransRow{From: strconv.Itoa(f), Total: total}
                // Tentukan top-3 untuk penanda "hot"
                topCounts := [10]int{}
                for t := 0; t <= 9; t++ {
                        topCounts[t] = trans[f][t]
                }
                threshold3 := 0
                tmp := make([]int, 10)
                copy(tmp, topCounts[:])
                sort.Sort(sort.Reverse(sort.IntSlice(tmp)))
                if len(tmp) >= 3 {
                        threshold3 = tmp[2]
                }
                for t := 0; t <= 9; t++ {
                        pct := 0
                        if total > 0 {
                                pct = int(float64(trans[f][t])*100/float64(total) + 0.5)
                        }
                        row.Trans[t] = EkorCell{
                                Count: trans[f][t],
                                Pct:   pct,
                                Hot:   trans[f][t] > 0 && trans[f][t] >= threshold3,
                        }
                }
                rows = append(rows, row)
        }

        // Cari ekor terakhir
        lastEkor, lastNomor, lastTgl := "", "", ""
        lastSesi := 0
        for i := len(entries) - 1; i >= 0; i-- {
                if len(entries[i].nomor) >= 4 {
                        lastNomor = entries[i].nomor
                        lastEkor = string(lastNomor[3])
                        lastTgl = entries[i].tanggal
                        lastSesi = entries[i].sesi
                        break
                }
        }

        total := 0
        for _, e := range entries {
                if len(e.nomor) >= 4 {
                        total++
                }
        }

        return &EkorStatsData{
                LastEkor:  lastEkor,
                LastNomor: lastNomor,
                LastTgl:   lastTgl,
                LastSesi:  lastSesi,
                Rows:      rows,
                EkorFreq:  ekorFreq,
                TotalData: total,
        }
}

func ekorStatsHandler(w http.ResponseWriter, r *http.Request) {
        render(w, "ekor-stats", PageData{
                EkorStats: calcEkorStats(),
        })
}

func apiResultsHandler(w http.ResponseWriter, r *http.Request) {
        limit := 50
        if l := r.URL.Query().Get("limit"); l != "" {
                limit, _ = strconv.Atoi(l)
        }
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(getResults(limit))
}

// ── Seed historis ─────────────────────────────────────────────────────────────

func seedPredictions() {
        var count int
        db.QueryRow(`SELECT COUNT(*) FROM bbfs_preds`).Scan(&count)
        if count > 0 {
                return
        }
        log.Println("Seeding prediksi historis...")
        rows, err := db.Query(
                `SELECT tanggal, sesi FROM results ORDER BY tanggal ASC, sesi ASC`)
        if err != nil {
                return
        }
        type slot struct {
                tanggal string
                sesi    int
        }
        var slots []slot
        for rows.Next() {
                var s slot
                rows.Scan(&s.tanggal, &s.sesi)
                slots = append(slots, s)
        }
        rows.Close()

        saved := 0
        for i, s := range slots {
                if i < 10 {
                        continue
                }
                stats := analyzeD2Enhanced(s.sesi)
                bbfs := buildBBFSFromStats(stats)
                if len(bbfs) < 5 {
                        continue
                }
                db.Exec(
                        `INSERT OR IGNORE INTO bbfs_preds (tanggal, sesi, digits, source) VALUES (?,?,?,?)`,
                        s.tanggal, s.sesi, bbfs, "AI-LOKAL")
                saved++
        }
        log.Printf("Seeded %d prediksi historis", saved)
}

// ── Main ──────────────────────────────────────────────────────────────────────

// cleanupBBFS2 hapus semua entry AI-LOKAL-2 dari database (tidak digunakan lagi)
func cleanupBBFS2() {
        res, err := db.Exec(`DELETE FROM bbfs_preds WHERE source='AI-LOKAL-2'`)
        if err != nil {
                return
        }
        n, _ := res.RowsAffected()
        if n > 0 {
                log.Printf("Cleanup: hapus %d entri AI-LOKAL-2 dari bbfs_preds", n)
        }
}

// migrateBBFS5to6 upgrade semua entry bbfs_preds 5-digit → 6-digit menggunakan algoritma baru
func migrateBBFS5to6() {
        rows, err := db.Query(`SELECT id, sesi FROM bbfs_preds WHERE source='AI-LOKAL' AND LENGTH(digits)=5`)
        if err != nil {
                return
        }
        type entry struct{ id, sesi int }
        var entries []entry
        for rows.Next() {
                var e entry
                rows.Scan(&e.id, &e.sesi)
                entries = append(entries, e)
        }
        rows.Close()
        if len(entries) == 0 {
                return
        }
        log.Printf("Migrasi %d entri BBFS 5-digit → 6-digit...", len(entries))
        for _, e := range entries {
                stats := analyzeD2Enhanced(e.sesi)
                newDigits := buildBBFSFromStats(stats)
                if len(newDigits) >= 6 {
                        db.Exec(`UPDATE bbfs_preds SET digits=? WHERE id=?`, newDigits, e.id)
                }
        }
        log.Printf("Migrasi BBFS selesai.")
}

// backfillPredComponents mengisi pred_components dari bbfs_preds+results historis
// agar sistem learning bisa langsung bekerja — dijalankan async agar tidak block startup
func backfillPredComponents() {
        go func() {
                rows, err := db.Query(`
                        SELECT bp.tanggal, bp.sesi, bp.digits, r.nomor
                        FROM bbfs_preds bp
                        INNER JOIN results r ON r.tanggal=bp.tanggal AND r.sesi=bp.sesi
                        WHERE bp.source='AI-LOKAL' AND LENGTH(bp.digits)=6 AND LENGTH(r.nomor)=4
                        AND NOT EXISTS (
                                SELECT 1 FROM pred_components pc 
                                WHERE pc.tanggal=bp.tanggal AND pc.sesi=bp.sesi
                        )`)
                if err != nil {
                        return
                }
                type row struct{ tanggal, digits, nomor string; sesi int }
                var buf []row
                for rows.Next() {
                        var r row
                        rows.Scan(&r.tanggal, &r.sesi, &r.digits, &r.nomor)
                        buf = append(buf, r)
                }
                rows.Close()
                if len(buf) == 0 {
                        return
                }
                tx, err := db.Begin()
                if err != nil {
                        return
                }
                stmt, err := tx.Prepare(`INSERT OR IGNORE INTO pred_components
                        (tanggal,sesi,top_d2,bbfs,markov_score,corr_score,total_score,is_hit,actual_d2)
                        VALUES (?,?,?,?,1.0,1.0,1.0,?,?)`)
                if err != nil {
                        tx.Rollback()
                        return
                }
                for _, r := range buf {
                        d2 := r.nomor[2:4]
                        hit := 0
                        if isHit(r.digits, d2) {
                                hit = 1
                        }
                        stmt.Exec(r.tanggal, r.sesi, d2, r.digits, hit, d2)
                }
                stmt.Close()
                tx.Commit()
                log.Printf("Backfill pred_components: %d rows terisi", len(buf))
                retuneWeights()
        }()
}

func main() {
        initDB()
        // Load learned weights ke memori supaya analyzeD2Enhanced bisa pakai
        globalWeightsMu.Lock()
        globalWeights = loadLearnedWeights()
        globalWeightsMu.Unlock()
        seedPredictions()
        cleanupBBFS2()
        migrateBBFS5to6()
        backfillPredComponents()
        loadTemplates()

        mux := http.NewServeMux()
        mux.HandleFunc("/", indexHandler)
        mux.HandleFunc("/input", inputHandler)
        mux.HandleFunc("/input-batch", inputBatchHandler)
        mux.HandleFunc("/paito", paitoHandler)
        mux.HandleFunc("/predict", predictHandler)
        mux.HandleFunc("/stats", statsHandler)
        mux.HandleFunc("/ekor-stats", ekorStatsHandler)
        mux.HandleFunc("/backtest", backtestHandler)
        mux.HandleFunc("/api/results", apiResultsHandler)
        mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

        log.Println("🎰 Toto Macau 4D Predictor → http://0.0.0.0:5000")
        log.Fatal(http.ListenAndServe("0.0.0.0:5000", recovery(mux)))
}
