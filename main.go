package main

import (
        "bytes"
        "database/sql"
        "encoding/json"
        "fmt"
        "html/template"
        "io"
        "log"
        "math/rand"
        "net/http"
        "os"
        "sort"
        "strconv"
        "strings"
        "time"

        _ "github.com/mattn/go-sqlite3"
)

// ── Models ────────────────────────────────────────────────────────────────────

type Result struct {
        ID        int
        Tanggal   string
        Sesi      int
        Nomor     string
        CreatedAt string
}

type Prediction struct {
        Metode    string
        NomorList string
        Numbers   []string
        BBFS      string
        Top2D     []string
        Alasan    string
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

type BBFSResult struct {
        Digits     string
        DigitList  []string
        Pairs2D    []string
        TotalPairs int
}

type FreqItem struct {
        D2    string
        Count int
        Color string
}

type PageData struct {
        Results     []Result
        Predictions []Prediction
        PaitoRows   []PaitoRow
        FreqItems   []FreqItem
        BBFS        *BBFSResult
        Message     string
        GeminiResp  string
        Error       string
        CurrentDate string
        CurrentSesi int
        NextSesi    int
        GeminiKey   string
        Stats       *Stats
        BBFSHistory []BBFSHistory
        InputDigits string
        FilterResult string
}

type BBFSHistory struct {
        Digits    string
        DigitList []string
        Source    string
        CreatedAt string
}

type Stats struct {
        Total int
        Hit2D int
        Rate2D float64
}

// ── DB ────────────────────────────────────────────────────────────────────────

var db *sql.DB

func initDB() {
        var err error
        db, err = sql.Open("sqlite3", "./toto.db")
        if err != nil {
                log.Fatal(err)
        }

        // Hanya butuh 2 tabel: results + bbfs_sessions
        // Hapus tabel lama yang tidak diperlukan
        db.Exec(`DROP TABLE IF EXISTS predictions`)
        db.Exec(`DROP TABLE IF EXISTS tune_history`)

        db.Exec(`CREATE TABLE IF NOT EXISTS results (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                tanggal TEXT NOT NULL,
                sesi INTEGER NOT NULL,
                nomor TEXT NOT NULL,
                created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
                UNIQUE(tanggal, sesi)
        )`)

        db.Exec(`CREATE TABLE IF NOT EXISTS bbfs_sessions (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                tanggal TEXT NOT NULL,
                sesi_start INTEGER NOT NULL,
                digits TEXT NOT NULL,
                source TEXT DEFAULT 'GEMINI',
                created_at DATETIME DEFAULT CURRENT_TIMESTAMP
        )`)
}

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
                                cell.Color = getColor(nomor)
                                if len(nomor) >= 4 {
                                        cell.D2 = nomor[2:]
                                }
                        } else {
                                cell.Nomor = "----"
                                cell.Color = "#374151"
                        }
                        row.Cells = append(row.Cells, cell)
                }
                paito = append(paito, row)
        }
        return paito
}

func getColor(nomor string) string {
        if len(nomor) < 2 {
                return "#374151"
        }
        d2 := nomor[len(nomor)-2:]
        n, err := strconv.Atoi(d2)
        if err != nil {
                return "#374151"
        }
        colors := []string{
                "#dc2626", "#ea580c", "#d97706", "#65a30d", "#16a34a",
                "#0d9488", "#0284c7", "#4f46e5", "#7c3aed", "#db2777",
        }
        return colors[(n/10)%10]
}

func analyzeFreq(limit int) []FreqItem {
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
                colors := []string{
                        "#dc2626","#ea580c","#d97706","#65a30d","#16a34a",
                        "#0d9488","#0284c7","#4f46e5","#7c3aed","#db2777",
                }
                items = append(items, FreqItem{
                        D2:    item.k,
                        Count: item.v,
                        Color: colors[(n/10)%10],
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

func calcStats() *Stats {
        var total int
        db.QueryRow(`SELECT COUNT(*) FROM results`).Scan(&total)
        return &Stats{Total: total}
}

// ── BBFS ─────────────────────────────────────────────────────────────────────

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

func getBBFSHistory() []BBFSHistory {
        rows, err := db.Query(
                `SELECT digits, source, created_at FROM bbfs_sessions ORDER BY id DESC LIMIT 10`)
        if err != nil {
                return nil
        }
        defer rows.Close()
        var hist []BBFSHistory
        for rows.Next() {
                var h BBFSHistory
                rows.Scan(&h.Digits, &h.Source, &h.CreatedAt)
                for _, c := range h.Digits {
                        h.DigitList = append(h.DigitList, string(c))
                }
                hist = append(hist, h)
        }
        return hist
}

// ── Gemini API ────────────────────────────────────────────────────────────────

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
        resp, err := http.Post(url, "application/json", bytes.NewReader(data))
        if err != nil {
                return "", err
        }
        defer resp.Body.Close()
        body, _ := io.ReadAll(resp.Body)
        var gr GeminiResponse
        if err := json.Unmarshal(body, &gr); err != nil {
                return "", err
        }
        if gr.Error != nil {
                return "", fmt.Errorf("Gemini error: %s", gr.Error.Message)
        }
        if len(gr.Candidates) == 0 || len(gr.Candidates[0].Content.Parts) == 0 {
                return "", fmt.Errorf("Gemini: respon kosong")
        }
        return gr.Candidates[0].Content.Parts[0].Text, nil
}

func buildPredictPrompt(tanggal string, sesi int, recents []string, freqs []FreqItem) string {
        top10 := []string{}
        for i, f := range freqs {
                if i >= 10 {
                        break
                }
                top10 = append(top10, fmt.Sprintf("%s(%dx)", f.D2, f.Count))
        }
        return fmt.Sprintf(`Kamu adalah analis prediksi Toto Macau 4D berpengalaman.

DATA HISTORIS (30 result terakhir, format 4D):
%s

FREKUENSI 2D TERTINGGI (dari 60 result terakhir):
%s

TARGET: %s Sesi %d

ANALISA & INSTRUKSI:
- Cari pola berulang pada digit 2D (2 digit terakhir)
- Perhatikan angka yang "belum muncul" (due number)
- Perhatikan pola hot/cold number
- Buat BBFS 5 digit yang paling optimal untuk mengcover 2D terkuat

FORMAT JAWABAN (wajib tepat seperti ini):
BBFS: [5 digit tanpa spasi, contoh: 12345]
TOP2D: [5 pasang 2D terbaik pisah koma, contoh: 23,45,12,67,89]
ALASAN: [analisa singkat max 2 kalimat]`,
                strings.Join(recents, " "),
                strings.Join(top10, ", "),
                tanggal, sesi)
}

func parseGeminiResp(text string) (bbfs string, top2D []string, alasan string) {
        for _, line := range strings.Split(text, "\n") {
                line = strings.TrimSpace(line)
                switch {
                case strings.HasPrefix(line, "BBFS:"):
                        raw := strings.TrimPrefix(line, "BBFS:")
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
                        raw := strings.TrimPrefix(line, "TOP2D:")
                        for _, p := range strings.Split(raw, ",") {
                                p = strings.TrimSpace(p)
                                // Ambil hanya 2 digit
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

// ── Handlers ──────────────────────────────────────────────────────────────────

// Gunakan map of template sets agar tiap halaman punya set sendiri,
// sehingga {{define "content"}} tidak saling override antar halaman.
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
                "join": strings.Join,
                "formatDate": func(s string) string {
                        t, err := time.Parse("2006-01-02", s)
                        if err != nil {
                                return s
                        }
                        days := []string{"Min", "Sen", "Sel", "Rab", "Kam", "Jum", "Sab"}
                        return days[t.Weekday()] + " " + t.Format("02/01/06")
                },
                "d2of": func(s string) string {
                        if len(s) >= 4 {
                                return s[2:4]
                        }
                        return "--"
                },
                "colorOf": getColor,
                "pct": func(count, max int) int {
                        if max == 0 {
                                return 0
                        }
                        return count * 100 / max
                },
        }
}

func loadTemplates() {
        pages := []string{"index", "input", "paito", "predict", "bbfs", "filter"}
        tmpls = make(map[string]*template.Template, len(pages))
        for _, p := range pages {
                tmpls[p] = template.Must(
                        template.New("").Funcs(newFuncMap()).ParseFiles(
                                "templates/base.html",
                                "templates/"+p+".html",
                        ))
        }
}

func render(w http.ResponseWriter, page string, data interface{}) {
        t, ok := tmpls[page]
        if !ok {
                http.Error(w, "template not found: "+page, 500)
                return
        }
        if err := t.ExecuteTemplate(w, page+".html", data); err != nil {
                log.Println("render error:", err)
        }
}

func currentSesi() int {
        h := time.Now().Hour()
        switch {
        case h >= 0 && h < 2:
                return 6
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
                return 1
        }
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
        msg := r.URL.Query().Get("msg")
        today := time.Now().Format("2006-01-02")
        sesi := currentSesi()
        next := sesi + 1
        if next > 6 {
                next = 1
        }
        data := PageData{
                Results:     getResults(30),
                PaitoRows:   buildPaito(14),
                CurrentDate: today,
                CurrentSesi: sesi,
                NextSesi:    next,
                Stats:       calcStats(),
                GeminiKey:   os.Getenv("GEMINI_API_KEY"),
                Message:     msg,
        }
        render(w, "index", data)
}

func inputHandler(w http.ResponseWriter, r *http.Request) {
        today := time.Now().Format("2006-01-02")
        if r.Method == "GET" {
                render(w, "input", PageData{CurrentDate: today})
                return
        }
        tanggal := r.FormValue("tanggal")
        sesi, _ := strconv.Atoi(r.FormValue("sesi"))
        nomor := strings.TrimSpace(r.FormValue("nomor"))
        if len(nomor) != 4 {
                render(w, "input", PageData{
                        Error: "Nomor harus tepat 4 digit!", CurrentDate: tanggal,
                })
                return
        }
        _, err := db.Exec(
                `INSERT OR REPLACE INTO results (tanggal, sesi, nomor) VALUES (?,?,?)`,
                tanggal, sesi, nomor)
        if err != nil {
                render(w, "input", PageData{
                        Error: "Gagal simpan: " + err.Error(), CurrentDate: tanggal,
                })
                return
        }
        http.Redirect(w, r, "/?msg=Result+"+nomor+"+berhasil+disimpan", http.StatusSeeOther)
}

func inputBatchHandler(w http.ResponseWriter, r *http.Request) {
        if r.Method != "POST" {
                http.Redirect(w, r, "/input", http.StatusSeeOther)
                return
        }
        batch := r.FormValue("batch")
        ok, fail := 0, 0
        for _, line := range strings.Split(batch, "\n") {
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
                _, err := db.Exec(
                        `INSERT OR REPLACE INTO results (tanggal, sesi, nomor) VALUES (?,?,?)`,
                        tanggal, sesi, nomor)
                if err != nil {
                        fail++
                } else {
                        ok++
                }
        }
        http.Redirect(w, r, fmt.Sprintf("/?msg=Batch+selesai:+%d+sukses,+%d+gagal", ok, fail),
                http.StatusSeeOther)
}

func paitoHandler(w http.ResponseWriter, r *http.Request) {
        days, _ := strconv.Atoi(r.URL.Query().Get("days"))
        if days <= 0 {
                days = 30
        }
        freqs := analyzeFreq(100)
        maxCnt := 0
        if len(freqs) > 0 {
                maxCnt = freqs[0].Count
        }
        data := PageData{
                PaitoRows: buildPaito(days),
                FreqItems: freqs,
                Stats:     &Stats{Total: maxCnt},
        }
        render(w, "paito", data)
}

func predictHandler(w http.ResponseWriter, r *http.Request) {
        today := time.Now().Format("2006-01-02")
        sesi := currentSesi()
        next := sesi + 1
        if next > 6 {
                next = 1
        }

        if r.Method == "GET" {
                render(w, "predict", PageData{
                        CurrentDate: today, NextSesi: next,
                        GeminiKey: os.Getenv("GEMINI_API_KEY"),
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
        if sesiTarget == 0 {
                sesiTarget = next
        }

        recents := getRecentNumbers(30)
        freqs := analyzeFreq(60)

        data := PageData{
                CurrentDate: tanggal,
                NextSesi:    sesiTarget,
                GeminiKey:   apiKey,
        }

        // ── Prediksi PAITO (murni dari frekuensi) ──
        paitoTop := []string{}
        for i, f := range freqs {
                if i >= 5 {
                        break
                }
                paitoTop = append(paitoTop, f.D2)
        }
        paitoPred := Prediction{
                Metode: "PAITO",
                Top2D:  paitoTop,
                Alasan: "Berdasar frekuensi 2D tertinggi dari data historis",
        }
        data.Predictions = append(data.Predictions, paitoPred)

        // ── Prediksi GEMINI ──
        if apiKey != "" {
                prompt := buildPredictPrompt(tanggal, sesiTarget, recents, freqs)
                resp, err := callGemini(apiKey, prompt)
                if err != nil {
                        data.Error = err.Error()
                } else {
                        data.GeminiResp = resp
                        bbfs, top2D, alasan := parseGeminiResp(resp)
                        gemPred := Prediction{
                                Metode: "GEMINI",
                                BBFS:   bbfs,
                                Top2D:  top2D,
                                Alasan: alasan,
                        }
                        data.Predictions = append(data.Predictions, gemPred)

                        // Generate BBFS
                        if len(bbfs) >= 5 {
                                data.BBFS = generateBBFS(bbfs)
                                db.Exec(
                                        `INSERT INTO bbfs_sessions (tanggal, sesi_start, digits, source) VALUES (?,?,?,?)`,
                                        tanggal, sesiTarget, bbfs, "GEMINI")
                        }
                }
        } else {
                // Fallback BBFS dari paito
                rng := rand.New(rand.NewSource(time.Now().UnixNano()))
                seen := map[string]bool{}
                var digs []string
                for _, f := range freqs {
                        for _, c := range f.D2 {
                                s := string(c)
                                if !seen[s] {
                                        seen[s] = true
                                        digs = append(digs, s)
                                }
                                if len(digs) == 5 {
                                        break
                                }
                        }
                        if len(digs) == 5 {
                                break
                        }
                }
                // Isi sisa dengan random jika kurang dari 5
                for len(digs) < 5 {
                        s := strconv.Itoa(rng.Intn(10))
                        if !seen[s] {
                                seen[s] = true
                                digs = append(digs, s)
                        }
                }
                data.BBFS = generateBBFS(strings.Join(digs, ""))
        }

        render(w, "predict", data)
}

func bbfsHandler(w http.ResponseWriter, r *http.Request) {
        today := time.Now().Format("2006-01-02")
        digits := r.URL.Query().Get("digits")
        if r.Method == "POST" {
                digits = r.FormValue("digits")
        }
        data := PageData{
                CurrentDate: today,
                InputDigits: digits,
                BBFSHistory: getBBFSHistory(),
        }
        if digits != "" {
                data.BBFS = generateBBFS(digits)
                if data.BBFS != nil && r.Method == "POST" {
                        sesi := currentSesi()
                        db.Exec(
                                `INSERT INTO bbfs_sessions (tanggal, sesi_start, digits, source) VALUES (?,?,?,?)`,
                                today, sesi, data.BBFS.Digits, "MANUAL")
                        data.BBFSHistory = getBBFSHistory()
                }
        }
        render(w, "bbfs", data)
}

func filterHandler(w http.ResponseWriter, r *http.Request) {
        if r.Method == "GET" {
                render(w, "filter", PageData{
                        GeminiKey: os.Getenv("GEMINI_API_KEY"),
                })
                return
        }
        apiKey := strings.TrimSpace(r.FormValue("api_key"))
        if apiKey == "" {
                apiKey = os.Getenv("GEMINI_API_KEY")
        }
        raw := r.FormValue("bbfs_list")
        var candidates []string
        for _, l := range strings.Split(raw, "\n") {
                l = strings.TrimSpace(l)
                if l != "" {
                        candidates = append(candidates, l)
                }
        }
        recents := getRecentNumbers(10)
        prompt := fmt.Sprintf(
                `Kamu adalah filter cerdas prediksi Toto Macau 4D.

KANDIDAT BBFS 5 DIGIT:
%s

HASIL TERBARU:
%s

TUGAS: Dari kandidat di atas, pilih 1 BBFS terbaik untuk 6 sesi ke depan.
Pertimbangkan: pola tidak berulang, hot number, angka yang belum muncul.

FORMAT JAWABAN:
BBFS_TERPILIH: [5 digit]
ALASAN: [max 2 kalimat analisa]`,
                strings.Join(candidates, "\n"),
                strings.Join(recents, " "))

        data := PageData{GeminiKey: apiKey}
        if apiKey != "" && len(candidates) > 0 {
                resp, err := callGemini(apiKey, prompt)
                if err != nil {
                        data.Error = err.Error()
                } else {
                        data.FilterResult = resp
                }
        } else {
                data.Error = "Masukkan API Key Gemini dan minimal 1 kandidat BBFS"
        }
        render(w, "filter", data)
}

func apiResultsHandler(w http.ResponseWriter, r *http.Request) {
        limit := 50
        if l := r.URL.Query().Get("limit"); l != "" {
                limit, _ = strconv.Atoi(l)
        }
        results := getResults(limit)
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(results)
}

func deleteResultHandler(w http.ResponseWriter, r *http.Request) {
        if r.Method != "POST" {
                http.Error(w, "Method not allowed", 405)
                return
        }
        id := r.FormValue("id")
        db.Exec(`DELETE FROM results WHERE id=?`, id)
        http.Redirect(w, r, "/input", http.StatusSeeOther)
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
        initDB()
        loadTemplates()

        mux := http.NewServeMux()
        mux.HandleFunc("/", indexHandler)
        mux.HandleFunc("/input", inputHandler)
        mux.HandleFunc("/input-batch", inputBatchHandler)
        mux.HandleFunc("/paito", paitoHandler)
        mux.HandleFunc("/predict", predictHandler)
        mux.HandleFunc("/bbfs", bbfsHandler)
        mux.HandleFunc("/filter", filterHandler)
        mux.HandleFunc("/api/results", apiResultsHandler)
        mux.HandleFunc("/delete-result", deleteResultHandler)
        mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

        log.Println("🎰 Toto Macau 4D Predictor → http://0.0.0.0:5000")
        log.Fatal(http.ListenAndServe("0.0.0.0:5000", mux))
}
