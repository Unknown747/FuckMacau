package main

import (
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

type PageData struct {
        Results           []Result
        Predictions       []Prediction
        PaitoRows         []PaitoRow
        FreqItems         []FreqItem
        BBFS              *BBFSResult
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
                if len(digits) == 5 {
                        break
                }
        }
        if len(digits) < 5 {
                return nil
        }
        var pairs []string
        for i := 0; i < 5; i++ {
                for j := 0; j < 5; j++ {
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

        // autoPredict boleh UPDATE prediksi sesi berikutnya karena result belum ada
        // (prediksi untuk sesi yang BELUM ada result-nya)
        stats := analyzeD2Enhanced(nextSesi)
        bbfs := buildBBFSFromStats(stats)
        if len(bbfs) < 5 {
                return ""
        }

        saveOrUpdatePrediction(nextDate, nextSesi, bbfs, "AI-LOKAL")

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
        rows, err := db.Query(`SELECT tanggal, sesi, digits FROM bbfs_preds`)
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
                err := db.QueryRow(
                        `SELECT nomor FROM results WHERE tanggal=? AND sesi=?`,
                        tanggal, sesi).Scan(&nomor)
                if err != nil || len(nomor) < 4 {
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

// dueGapBonus menghitung bonus "due" berdasarkan gap sejak terakhir muncul di sesi ini
func dueGapBonus(sesiOnly []string, d2 string) float64 {
        for i := len(sesiOnly) - 1; i >= 0; i-- {
                if sesiOnly[i] == d2 {
                        gap := len(sesiOnly) - 1 - i
                        if gap == 0 {
                                return 0
                        }
                        // gap besar = due → bonus besar, tapi capped
                        return math.Min(math.Log(float64(gap+1))*3.0, 12.0)
                }
        }
        // Belum pernah muncul di sesi ini — berikan bonus eksplorasi kecil saja
        // (jangan terlalu tinggi agar tidak mengalahkan D2 yang memang sering muncul)
        return 3.5
}

func analyzeD2Enhanced(targetSesi int) []D2Stat {
        now := nowWIB()
        allEntries := getAllResults()
        if len(allEntries) == 0 {
                return nil
        }

        // ── (1) Batasi hanya 45 hari terakhir ────────────────────────────────────
        cutoff := now.AddDate(0, 0, -45)
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

        // ── (3) Bobot sesiFreq dinamis dari akurasi historis sesi ini ────────────
        sesiWeight := calcSesiWeight(targetSesi)

        type stat struct {
                allFreq     float64
                sesiFreq    float64
                lastIdx     int
                lastSesiIdx int
                markov      float64
                streak      float64
                due         float64
        }
        stats := map[string]*stat{}
        ensure := func(d2 string) {
                if stats[d2] == nil {
                        stats[d2] = &stat{lastIdx: 9999, lastSesiIdx: 9999}
                }
        }

        // Urutan D2 seluruh riwayat
        allD2Sequence := []string{}
        for _, e := range allEntries {
                if len(e.nomor) >= 4 {
                        allD2Sequence = append(allD2Sequence, e.nomor[2:4])
                }
        }

        // Frekuensi semua sesi — decay tajam: 7 hari terakhir × 8, sisanya turun cepat
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
                        case age <= 7:
                                weight = 8.0 - age*0.5 // 8.0 → 4.5 dalam 7 hari
                        case age <= 30:
                                weight = math.Exp(-age/15.0)*3.0 + 0.3
                        default:
                                weight = math.Exp(-age/45.0)*1.0 + 0.2
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
                        case age <= 7:
                                weight = 10.0 - age*0.6 // bobot sangat tinggi untuk 7 hari terakhir
                        case age <= 21:
                                weight = math.Exp(-age/10.0)*5.0 + 0.4
                        default:
                                weight = math.Exp(-age/30.0)*2.0 + 0.2
                        }
                }
                stats[d2].sesiFreq += weight
                if stats[d2].lastSesiIdx == 9999 {
                        stats[d2].lastSesiIdx = len(sesiEntries) - i
                }
        }

        // Streak & Due per sesi
        seenD2 := map[string]bool{}
        for _, d2 := range sesiOnly {
                seenD2[d2] = true
        }
        for d2 := range seenD2 {
                ensure(d2)
                stats[d2].streak = streakBonus(sesiOnly, d2)
                stats[d2].due = dueGapBonus(sesiOnly, d2)
        }
        // Digit yang belum pernah muncul di sesi ini juga dapat due bonus
        for _, d := range []string{"00","01","02","03","04","05","06","07","08","09",
                "10","11","12","13","14","15","16","17","18","19",
                "20","21","22","23","24","25","26","27","28","29",
                "30","31","32","33","34","35","36","37","38","39",
                "40","41","42","43","44","45","46","47","48","49",
                "50","51","52","53","54","55","56","57","58","59",
                "60","61","62","63","64","65","66","67","68","69",
                "70","71","72","73","74","75","76","77","78","79",
                "80","81","82","83","84","85","86","87","88","89",
                "90","91","92","93","94","95","96","97","98","99"} {
                if !seenD2[d] {
                        ensure(d)
                        stats[d].due = 8.0
                }
        }

        // Markov Chain umum: gunakan D2 terakhir dari sesi sebelumnya (lebih relevan)
        markovGen := markovTransition(allEntries)
        prevSesi := targetSesi - 1
        if prevSesi < 1 {
                prevSesi = 6
        }
        // Cari result terakhir dari prevSesi (bukan global last)
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
                                stats[d2].markov += prob * 6.0
                        }
                }
        }

        // Markov transisi sesi N-1 → sesi N (bobot tinggi, hanya sesi yang sama hari)
        for d2, prob := range markovSesiTransition(allEntries, prevSesi, targetSesi) {
                ensure(d2)
                stats[d2].markov += prob * 8.0
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

        // Gabungkan skor — hanya D2 yang pernah muncul di sesi ini atau punya Markov score
        // Gunakan sesiWeight (auto-learning) sebagai pengganti bobot 6.0 yang statis
        var list []D2Stat
        for d2, s := range stats {
                // Filter: buang D2 yang benar-benar tidak pernah muncul dan tidak ada sinyal
                if s.sesiFreq == 0 && s.allFreq == 0 && s.markov < 0.5 {
                        continue
                }
                score := s.sesiFreq*sesiWeight + s.allFreq*1.5 + s.markov + s.streak + s.due*0.8
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

        allDigits := []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9"}

        // Exhaustive search C(10,5) = 252 kombinasi
        bestScore := -1.0
        bestCombo := []string{}

        for i := 0; i < 10; i++ {
                for j := i + 1; j < 10; j++ {
                        for k := j + 1; k < 10; k++ {
                                for l := k + 1; l < 10; l++ {
                                        for m := l + 1; m < 10; m++ {
                                                combo := []string{
                                                        allDigits[i], allDigits[j], allDigits[k],
                                                        allDigits[l], allDigits[m],
                                                }
                                                // Hitung total skor semua 20 pasangan D2 dari 5 digit ini
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

        if len(bestCombo) == 0 {
                // Fallback: ambil 5 digit dengan kontribusi tertinggi
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
                        if len(bestCombo) == 5 {
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
                "formatDate": func(s string) string {
                        t, err := time.Parse("2006-01-02", s)
                        if err != nil {
                                return s
                        }
                        days := []string{"Min", "Sen", "Sel", "Rab", "Kam", "Jum", "Sab"}
                        return days[t.Weekday()] + " " + t.Format("02/01")
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
        pages := []string{"index", "input", "paito", "predict", "stats"}
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
        w.Header().Set("Content-Type", "text/html; charset=utf-8")
        if err := t.ExecuteTemplate(w, page+".html", data); err != nil {
                log.Println("render error:", err)
        }
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

func currentSesi() int {
        h := nowWIB().Hour()
        switch {
        case h >= 0 && h < 13:
                return 1 // result 00:01
        case h >= 13 && h < 16:
                return 2 // result 13:00
        case h >= 16 && h < 19:
                return 3 // result 16:00
        case h >= 19 && h < 22:
                return 4 // result 19:00
        case h >= 22 && h < 23:
                return 5 // result 22:00
        default:
                return 6 // result 23:00
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
                // Koreksi: jika sesi yang dihitung sudah berlalu berdasarkan jam WIB,
                // majukan ke sesi berikutnya setelah sesi aktif sekarang.
                // Contoh: last DB = sesi 3, tapi jam sudah 20:00 (sesi 4 sudah keluar)
                // → nextSesi seharusnya 5, bukan 4.
                nowStr := nowWIB().Format("2006-01-02")
                if nextDate == nowStr && nextSesi <= currentSesi() {
                        nextDate, nextSesi = nextSessionAfter(nowStr, currentSesi())
                }
                pred = getPredictionForDate(nextDate, nextSesi)
        } else {
                // Belum ada data, fallback ke jam WIB
                clockSesi := currentSesi()
                clockNext := clockSesi%6 + 1
                nextSesi = clockNext
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

        if len(nomor) != 4 {
                render(w, "input", PageData{Error: "Nomor harus tepat 4 digit!", CurrentDate: tanggal, Results: getResults(30)})
                return
        }
        if _, err := db.Exec(
                `INSERT OR REPLACE INTO results (tanggal, sesi, nomor) VALUES (?,?,?)`,
                tanggal, sesi, nomor); err != nil {
                render(w, "input", PageData{Error: "Gagal simpan: " + err.Error(), CurrentDate: tanggal, Results: getResults(30)})
                return
        }

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
                if len(nomor) != 4 || sesi < 1 || sesi > 6 {
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
        // Prediksi selalu otomatis — tidak ada POST/generate manual
        // Ambil sesi berikutnya berdasarkan result terakhir di DB
        lastDate, lastSesi := getLastResultEntry()
        today := nowWIB().Format("2006-01-02")
        next := currentSesi()%6 + 1

        if lastDate != "" {
                var d string
                var s int
                d, s = nextSessionAfter(lastDate, lastSesi)
                today = d
                next = s
                // Koreksi: jika sesi yang dihitung sudah berlalu berdasarkan jam WIB,
                // majukan ke sesi berikutnya setelah sesi aktif sekarang.
                nowStr := nowWIB().Format("2006-01-02")
                if today == nowStr && next <= currentSesi() {
                        today, next = nextSessionAfter(nowStr, currentSesi())
                }
        }

        // Override jika ada query param ?tanggal=&sesi= (untuk lihat sesi lain, read-only)
        if qt := r.URL.Query().Get("tanggal"); qt != "" {
                today = qt
        }
        if qs := r.URL.Query().Get("sesi"); qs != "" {
                if sv, err := strconv.Atoi(qs); err == nil && sv >= 1 && sv <= 6 {
                        next = sv
                }
        }

        pred := getPredictionForDate(today, next)

        paitoStats := analyzeD2Enhanced(next)
        top10 := paitoStats
        if len(top10) > 10 {
                top10 = top10[:10]
        }

        var bbfs string
        if pred != nil {
                bbfs = pred.BBFS
                pred.Top2D = top10
                pred.Alasan = buildAlasan(top10, next)
        } else {
                bbfs = buildBBFSFromStats(paitoStats)
                if len(bbfs) >= 5 {
                        savePrediction(today, next, bbfs, "AI-LOKAL")
                        pred = &Prediction{
                                Metode:   "AI-LOKAL",
                                Top2D:    top10,
                                BBFS:     bbfs,
                                BBFSList: strings.Split(bbfs, ""),
                                Alasan:   buildAlasan(top10, next),
                        }
                }
        }

        data := PageData{
                CurrentDate: today,
                NextSesi:    next,
        }
        if pred != nil {
                data.Predictions = []Prediction{*pred}
        }
        if len(bbfs) >= 5 {
                data.BBFS = generateBBFS(bbfs)
        }

        render(w, "predict", data)
}

func statsHandler(w http.ResponseWriter, r *http.Request) {
        render(w, "stats", PageData{
                SesiStats: calcSesiStats(),
                WR:        calcWinRate(),
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

func main() {
        initDB()
        seedPredictions()
        loadTemplates()

        mux := http.NewServeMux()
        mux.HandleFunc("/", indexHandler)
        mux.HandleFunc("/input", inputHandler)
        mux.HandleFunc("/input-batch", inputBatchHandler)
        mux.HandleFunc("/paito", paitoHandler)
        mux.HandleFunc("/predict", predictHandler)
        mux.HandleFunc("/stats", statsHandler)
        mux.HandleFunc("/api/results", apiResultsHandler)
        mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

        log.Println("🎰 Toto Macau 4D Predictor → http://0.0.0.0:5000")
        log.Fatal(http.ListenAndServe("0.0.0.0:5000", recovery(mux)))
}
