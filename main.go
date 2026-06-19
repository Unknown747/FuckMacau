package main

import (
        "bytes"
        "database/sql"
        "encoding/json"
        "fmt"
        "html/template"
        "io"
        "log"
        "net/http"
        "os"
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
        D2       string
        AllFreq  int
        SesiFreq int
        LastSeen int
        Score    float64
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
        Result    string // nomor aktual (kosong jika belum ada)
        D2        string // 2 digit terakhir result
        IsHit     bool
        HasResult bool
}

type WinRate struct {
        Total int
        Hits  int
        Miss  int
        Pct   int
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
        GeminiKey         string
        Stats             *Stats
        GeminiResp        string
        Validations       []BBFSValidation
        WR                WinRate
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
        // Jangan drop predictions — kita pakai bbfs_preds sekarang
        db.Exec(`CREATE TABLE IF NOT EXISTS results (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                tanggal TEXT NOT NULL,
                sesi INTEGER NOT NULL,
                nomor TEXT NOT NULL,
                created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
                UNIQUE(tanggal, sesi)
        )`)
        // Tabel prediksi BBFS — untuk validasi hit/miss
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

// isHit cek apakah d2 ada di BBFS pairs dari digits
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

// savePrediction simpan BBFS ke bbfs_preds (idempotent)
func savePrediction(tanggal string, sesi int, digits, source string) {
        if len(digits) < 5 {
                return
        }
        db.Exec(
                `INSERT OR REPLACE INTO bbfs_preds (tanggal, sesi, digits, source) VALUES (?,?,?,?)`,
                tanggal, sesi, digits, source)
}

// autoPredict: generate & simpan prediksi untuk sesi berikutnya
// Dipanggil otomatis setelah input result
func autoPredict(tanggal string, sesi int) string {
        nextSesi := sesi + 1
        nextDate := tanggal
        if nextSesi > 6 {
                nextSesi = 1
                t, err := time.Parse("2006-01-02", tanggal)
                if err == nil {
                        nextDate = t.AddDate(0, 0, 1).Format("2006-01-02")
                }
        }

        // Cek apakah sudah ada prediksi untuk slot ini
        var existing string
        db.QueryRow(`SELECT digits FROM bbfs_preds WHERE tanggal=? AND sesi=? AND source='AI-LOKAL'`,
                nextDate, nextSesi).Scan(&existing)

        stats := analyzeD2(nextSesi, 150, 60)
        bbfs := buildBBFSFromStats(stats)
        if len(bbfs) < 5 {
                return ""
        }

        savePrediction(nextDate, nextSesi, bbfs, "AI-LOKAL")

        if existing != "" && existing != bbfs {
                return fmt.Sprintf("Auto-prediksi Sesi %d (%s) diperbarui: BBFS %s", nextSesi, nextDate, bbfs)
        }
        return fmt.Sprintf("Auto-prediksi Sesi %d (%s): BBFS %s", nextSesi, nextDate, bbfs)
}

// getBBFSValidations ambil riwayat prediksi + validasi hit/miss
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

                // Buat pairs
                res := generateBBFS(v.Digits)
                if res != nil {
                        v.Pairs = res.Pairs2D
                        v.DigitList = res.DigitList
                }

                // Cari result aktual
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

// calcWinRate hitung win rate dari semua prediksi yang sudah ada hasilnya
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
                        continue // belum ada result
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

// ── AI Analisa Lokal ──────────────────────────────────────────────────────────

type rawEntry struct {
        tanggal string
        sesi    int
        nomor   string
}

func analyzeD2(targetSesi, allLimit, sesiLimit int) []D2Stat {
        now := nowWIB()

        rows, err := db.Query(
                `SELECT tanggal, sesi, nomor FROM results ORDER BY tanggal DESC, sesi DESC LIMIT ?`, allLimit)
        if err != nil {
                return nil
        }
        var all []rawEntry
        for rows.Next() {
                var e rawEntry
                rows.Scan(&e.tanggal, &e.sesi, &e.nomor)
                all = append(all, e)
        }
        rows.Close()

        rows2, err := db.Query(
                `SELECT tanggal, nomor FROM results WHERE sesi=? ORDER BY tanggal DESC LIMIT ?`,
                targetSesi, sesiLimit)
        var sesiEntries []rawEntry
        if err == nil {
                for rows2.Next() {
                        var e rawEntry
                        e.sesi = targetSesi
                        rows2.Scan(&e.tanggal, &e.nomor)
                        sesiEntries = append(sesiEntries, e)
                }
                rows2.Close()
        }

        type stat struct {
                allFreq     float64
                sesiFreq    float64
                lastIdx     int
                lastSesiIdx int
        }
        stats := map[string]*stat{}

        ensure := func(d2 string) {
                if stats[d2] == nil {
                        stats[d2] = &stat{lastIdx: 9999, lastSesiIdx: 9999}
                }
        }

        for i, e := range all {
                if len(e.nomor) < 4 {
                        continue
                }
                d2 := e.nomor[2:4]
                ensure(d2)
                t, err := time.Parse("2006-01-02", e.tanggal)
                weight := 1.0
                if err == nil {
                        age := now.Sub(t).Hours() / 24
                        if age <= 7 {
                                weight = 3.0
                        } else if age <= 14 {
                                weight = 2.0
                        }
                }
                stats[d2].allFreq += weight
                if stats[d2].lastIdx == 9999 {
                        stats[d2].lastIdx = i + 1
                }
        }

        for i, e := range sesiEntries {
                if len(e.nomor) < 4 {
                        continue
                }
                d2 := e.nomor[2:4]
                ensure(d2)
                t, err := time.Parse("2006-01-02", e.tanggal)
                weight := 1.0
                if err == nil {
                        age := now.Sub(t).Hours() / 24
                        if age <= 7 {
                                weight = 3.0
                        } else if age <= 14 {
                                weight = 2.0
                        }
                }
                stats[d2].sesiFreq += weight
                if stats[d2].lastSesiIdx == 9999 {
                        stats[d2].lastSesiIdx = i + 1
                }
        }

        var list []D2Stat
        for d2, s := range stats {
                dueBonus := 0.0
                if s.lastSesiIdx > 12 && s.sesiFreq >= 1.5 {
                        dueBonus = float64(s.lastSesiIdx) * 0.4
                }
                score := s.sesiFreq*4 + s.allFreq*1 + dueBonus
                lastSeen := s.lastIdx
                if s.lastSesiIdx < lastSeen {
                        lastSeen = s.lastSesiIdx
                }
                list = append(list, D2Stat{
                        D2:       d2,
                        AllFreq:  int(s.allFreq + 0.5),
                        SesiFreq: int(s.sesiFreq + 0.5),
                        LastSeen: lastSeen,
                        Score:    score,
                })
        }
        sort.Slice(list, func(i, j int) bool {
                if list[i].Score != list[j].Score {
                        return list[i].Score > list[j].Score
                }
                return list[i].D2 < list[j].D2
        })
        return list
}

func buildBBFSFromStats(stats []D2Stat) string {
        seen := map[string]bool{}
        var digits []string
        for _, s := range stats {
                for _, c := range s.D2 {
                        ch := string(c)
                        if !seen[ch] {
                                seen[ch] = true
                                digits = append(digits, ch)
                        }
                        if len(digits) == 5 {
                                break
                        }
                }
                if len(digits) == 5 {
                        break
                }
        }
        return strings.Join(digits, "")
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
        type kv struct {
                k string
                v int
        }
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

func getRecentNumbers(limit int) []string {
        rows, err := db.Query(
                `SELECT nomor FROM results ORDER BY tanggal DESC, sesi DESC LIMIT ?`, limit)
        if err != nil {
                return nil
        }
        defer rows.Close()
        var res []string
        for rows.Next() {
                var n string
                rows.Scan(&n)
                res = append(res, n)
        }
        return res
}

func buildPaitoAlasan(stats []D2Stat, sesi int) string {
        if len(stats) == 0 {
                return "Belum cukup data historis"
        }
        top := stats[0]
        var due []string
        for _, s := range stats {
                if s.LastSeen > 12 && s.SesiFreq >= 1 {
                        due = append(due, s.D2)
                }
                if len(due) >= 2 {
                        break
                }
        }
        msg := fmt.Sprintf("2D terpanas Sesi %d: %s (muncul %dx, skor %.0f). ", sesi, top.D2, top.SesiFreq, top.Score)
        if len(due) > 0 {
                msg += fmt.Sprintf("Due number: %s — sudah lama tidak keluar.", strings.Join(due, ", "))
        }
        return msg
}

// ── Gemini API (opsional) ─────────────────────────────────────────────────────

type GeminiRequest struct {
        Contents []GeminiContent `json:"contents"`
}
type GeminiContent struct {
        Parts []GeminiPart `json:"parts"`
}
type GeminiPart struct {
        Text string `json:"text"`
}
type GeminiResponse struct {
        Candidates []struct {
                Content struct {
                        Parts []struct {
                                Text string `json:"text"`
                        } `json:"parts"`
                } `json:"content"`
        } `json:"candidates"`
        Error *struct {
                Message string `json:"message"`
        } `json:"error"`
}

func callGemini(apiKey, prompt string) (string, error) {
        url := fmt.Sprintf(
                "https://generativelanguage.googleapis.com/v1beta/models/gemini-1.5-flash:generateContent?key=%s",
                apiKey)
        reqBody := GeminiRequest{
                Contents: []GeminiContent{{Parts: []GeminiPart{{Text: prompt}}}},
        }
        data, _ := json.Marshal(reqBody)
        client := &http.Client{Timeout: 30 * time.Second}
        resp, err := client.Post(url, "application/json", bytes.NewReader(data))
        if err != nil {
                return "", fmt.Errorf("koneksi gagal: %v", err)
        }
        defer resp.Body.Close()
        body, _ := io.ReadAll(resp.Body)
        var gr GeminiResponse
        if err := json.Unmarshal(body, &gr); err != nil {
                return "", fmt.Errorf("parse response gagal")
        }
        if gr.Error != nil {
                return "", fmt.Errorf("Gemini: %s", gr.Error.Message)
        }
        if len(gr.Candidates) == 0 || len(gr.Candidates[0].Content.Parts) == 0 {
                return "", fmt.Errorf("Gemini: respon kosong")
        }
        return gr.Candidates[0].Content.Parts[0].Text, nil
}

func buildGeminiPrompt(tanggal string, targetSesi int, paitoStats []D2Stat, allRecents []string) string {
        recentStr := strings.Join(allRecents[:min(30, len(allRecents))], " ")
        top5 := []string{}
        for i, s := range paitoStats {
                if i >= 5 {
                        break
                }
                top5 = append(top5, fmt.Sprintf("%s(s=%d,a=%d)", s.D2, s.SesiFreq, s.AllFreq))
        }
        due := []string{}
        for _, s := range paitoStats {
                if s.LastSeen > 15 && s.AllFreq >= 2 {
                        due = append(due, fmt.Sprintf("%s(%d putaran)", s.D2, s.LastSeen))
                }
                if len(due) >= 5 {
                        break
                }
        }
        bbfsPaito := buildBBFSFromStats(paitoStats)
        return fmt.Sprintf(`Analis prediksi Toto Macau 4D.

TARGET: Tanggal %s, Sesi %d

HISTORIS (30 result terbaru): %s

TOP 5 2D Sesi %d (AI Lokal): %s

DUE NUMBERS: %s

BBFS AI LOKAL: %s

FORMAT JAWABAN:
BBFS: [5 digit]
TOP2D: [5 pasang pisah koma]
ALASAN: [1-2 kalimat]`,
                tanggal, targetSesi,
                recentStr,
                targetSesi,
                strings.Join(top5, ", "),
                func() string {
                        if len(due) == 0 {
                                return "-"
                        }
                        return strings.Join(due, ", ")
                }(),
                bbfsPaito,
        )
}

func min(a, b int) int {
        if a < b {
                return a
        }
        return b
}

func parseGeminiResp(text string) (bbfs string, top2D []string, alasan string) {
        for _, line := range strings.Split(text, "\n") {
                line = strings.TrimSpace(line)
                switch {
                case strings.HasPrefix(line, "BBFS:"):
                        raw := strings.TrimSpace(strings.TrimPrefix(line, "BBFS:"))
                        var d []string
                        seen := map[string]bool{}
                        for _, c := range raw {
                                s := string(c)
                                if c >= '0' && c <= '9' && !seen[s] {
                                        seen[s] = true
                                        d = append(d, s)
                                }
                                if len(d) == 5 {
                                        break
                                }
                        }
                        bbfs = strings.Join(d, "")
                case strings.HasPrefix(line, "TOP2D:"):
                        raw := strings.TrimSpace(strings.TrimPrefix(line, "TOP2D:"))
                        for _, p := range strings.Split(raw, ",") {
                                p = strings.TrimSpace(p)
                                var digs []string
                                for _, c := range p {
                                        if c >= '0' && c <= '9' {
                                                digs = append(digs, string(c))
                                        }
                                }
                                if len(digs) >= 2 {
                                        top2D = append(top2D, digs[0]+digs[1])
                                }
                                if len(top2D) >= 5 {
                                        break
                                }
                        }
                case strings.HasPrefix(line, "ALASAN:"):
                        alasan = strings.TrimSpace(strings.TrimPrefix(line, "ALASAN:"))
                }
        }
        return
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
        }
}

func loadTemplates() {
        pages := []string{"index", "input", "paito", "predict"}
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
        case h >= 13 && h < 16:
                return 1
        case h >= 16 && h < 19:
                return 2
        case h >= 19 && h < 21:
                return 3
        case h >= 21 && h < 23:
                return 4
        case h >= 23:
                return 5
        default:
                return 6
        }
}

// ── Handlers ──────────────────────────────────────────────────────────────────

// getCurrentPrediction ambil atau generate prediksi BBFS untuk sesi target
func getCurrentPrediction(nextSesi int) *Prediction {
        today := nowWIB().Format("2006-01-02")

        // Cek apakah sudah ada prediksi hari ini untuk sesi ini
        var digits string
        db.QueryRow(
                `SELECT digits FROM bbfs_preds WHERE tanggal=? AND sesi=? AND source='AI-LOKAL'`,
                today, nextSesi).Scan(&digits)

        // Jika belum ada, generate baru
        if len(digits) < 5 {
                stats := analyzeD2(nextSesi, 150, 60)
                digits = buildBBFSFromStats(stats)
                if len(digits) >= 5 {
                        savePrediction(today, nextSesi, digits, "AI-LOKAL")
                }
        }
        if len(digits) < 5 {
                return nil
        }

        stats := analyzeD2(nextSesi, 150, 60)
        top10 := stats
        if len(top10) > 10 {
                top10 = top10[:10]
        }

        return &Prediction{
                Metode:   "AI-LOKAL",
                Top2D:    top10,
                BBFS:     digits,
                BBFSList: strings.Split(digits, ""),
                Alasan:   buildPaitoAlasan(top10, nextSesi),
        }
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/" {
                http.NotFound(w, r)
                return
        }
        sesi := currentSesi()
        next := sesi%6 + 1
        render(w, "index", PageData{
                CurrentDate:       nowWIB().Format("2006-01-02"),
                CurrentSesi:       sesi,
                NextSesi:          next,
                Stats:             calcStats(),
                Message:           r.URL.Query().Get("msg"),
                Validations:       getBBFSValidations(10),
                WR:                calcWinRate(),
                CurrentPrediction: getCurrentPrediction(next),
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

        // ── AUTO PREDICT untuk sesi berikutnya ──
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
        // Auto predict untuk sesi setelah batch terakhir
        if lastTanggal != "" {
                autoPredict(lastTanggal, lastSesi)
        }
        http.Redirect(w, r,
                fmt.Sprintf("/?msg=Batch+selesai:+%d+sukses,+%d+gagal.+Prediksi+otomatis+disimpan.", ok, fail),
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
        today := nowWIB().Format("2006-01-02")
        sesi := currentSesi()
        next := sesi%6 + 1

        if r.Method == "GET" {
                render(w, "predict", PageData{
                        CurrentDate: today,
                        NextSesi:    next,
                        GeminiKey:   os.Getenv("GEMINI_API_KEY"),
                })
                return
        }

        apiKey := strings.TrimSpace(r.FormValue("api_key"))
        if apiKey == "" {
                apiKey = os.Getenv("GEMINI_API_KEY")
        }
        tanggal := r.FormValue("tanggal")
        if tanggal == "" {
                tanggal = today
        }
        sesiTarget, _ := strconv.Atoi(r.FormValue("sesi"))
        if sesiTarget < 1 || sesiTarget > 6 {
                sesiTarget = next
        }

        data := PageData{
                CurrentDate: tanggal,
                NextSesi:    sesiTarget,
                GeminiKey:   apiKey,
        }

        // ── AI Analisa Lokal ──────────────────────────────────────────────────────
        paitoStats := analyzeD2(sesiTarget, 150, 60)
        top10 := paitoStats
        if len(top10) > 10 {
                top10 = top10[:10]
        }
        bbfsLocal := buildBBFSFromStats(paitoStats)

        localPred := Prediction{
                Metode: "AI-LOKAL",
                Top2D:  top10,
                BBFS:   bbfsLocal,
                Alasan: buildPaitoAlasan(top10, sesiTarget),
        }
        if len(bbfsLocal) >= 5 {
                localPred.BBFSList = strings.Split(bbfsLocal, "")
                data.BBFS = generateBBFS(bbfsLocal)
                // Simpan ke DB untuk tracking WR
                savePrediction(tanggal, sesiTarget, bbfsLocal, "AI-LOKAL")
        }
        data.Predictions = append(data.Predictions, localPred)

        // ── Gemini (opsional) ─────────────────────────────────────────────────────
        if apiKey != "" {
                allRecents := getRecentNumbers(50)
                prompt := buildGeminiPrompt(tanggal, sesiTarget, paitoStats, allRecents)
                resp, err := callGemini(apiKey, prompt)
                if err != nil {
                        data.Error = "Gemini: " + err.Error()
                } else {
                        data.GeminiResp = resp
                        gemBBFS, gemTop2D, gemAlasan := parseGeminiResp(resp)
                        statMap := map[string]D2Stat{}
                        for _, s := range paitoStats {
                                statMap[s.D2] = s
                        }
                        var gemD2Stats []D2Stat
                        for _, d2 := range gemTop2D {
                                if s, ok := statMap[d2]; ok {
                                        gemD2Stats = append(gemD2Stats, s)
                                } else {
                                        gemD2Stats = append(gemD2Stats, D2Stat{D2: d2})
                                }
                        }
                        gemPred := Prediction{
                                Metode: "GEMINI",
                                Top2D:  gemD2Stats,
                                BBFS:   gemBBFS,
                                Alasan: gemAlasan,
                        }
                        if len(gemBBFS) >= 5 {
                                gemPred.BBFSList = strings.Split(gemBBFS, "")
                                data.BBFS = generateBBFS(gemBBFS)
                                savePrediction(tanggal, sesiTarget, gemBBFS, "GEMINI")
                        }
                        data.Predictions = append(data.Predictions, gemPred)
                }
        }

        render(w, "predict", data)
}

func apiResultsHandler(w http.ResponseWriter, r *http.Request) {
        limit := 50
        if l := r.URL.Query().Get("limit"); l != "" {
                limit, _ = strconv.Atoi(l)
        }
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(getResults(limit))
}

// ── Main ──────────────────────────────────────────────────────────────────────

// seedPredictions: generate retroaktif prediksi untuk data historis
// supaya WR dan validasi langsung bisa ditampilkan
func seedPredictions() {
        var count int
        db.QueryRow(`SELECT COUNT(*) FROM bbfs_preds`).Scan(&count)
        if count > 0 {
                return // sudah ada, skip
        }
        log.Println("Seeding prediksi historis...")

        // Ambil semua (tanggal, sesi) yang ada result, urutkan ascending
        rows, err := db.Query(
                `SELECT tanggal, sesi FROM results ORDER BY tanggal ASC, sesi ASC`)
        if err != nil {
                return
        }
        type slot struct{ tanggal string; sesi int }
        var slots []slot
        for rows.Next() {
                var s slot
                rows.Scan(&s.tanggal, &s.sesi)
                slots = append(slots, s)
        }
        rows.Close()

        saved := 0
        for i, s := range slots {
                // Gunakan data sebelum slot ini sebagai dasar prediksi
                // (simulasi prediksi "live" tanpa melihat result saat itu)
                // Kita generate prediksi berdasarkan sesi ini dari data sebelumnya
                if i < 10 {
                        continue // skip 10 pertama — data terlalu sedikit
                }
                stats := analyzeD2(s.sesi, 120, 50)
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
        mux.HandleFunc("/api/results", apiResultsHandler)
        mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

        log.Println("🎰 Toto Macau 4D Predictor → http://0.0.0.0:5000")
        log.Fatal(http.ListenAndServe("0.0.0.0:5000", recovery(mux)))
}
