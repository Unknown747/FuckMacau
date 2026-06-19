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
	Stats             *Stats
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

func savePrediction(tanggal string, sesi int, digits, source string) {
	if len(digits) < 5 {
		return
	}
	db.Exec(
		`INSERT OR REPLACE INTO bbfs_preds (tanggal, sesi, digits, source) VALUES (?,?,?,?)`,
		tanggal, sesi, digits, source)
}

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

	var existing string
	db.QueryRow(`SELECT digits FROM bbfs_preds WHERE tanggal=? AND sesi=? AND source='AI-LOKAL'`,
		nextDate, nextSesi).Scan(&existing)

	stats := analyzeD2Enhanced(nextSesi)
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

// ── AI Lokal: Analisa data mentah ─────────────────────────────────────────────

type rawEntry struct {
	tanggal string
	sesi    int
	nomor   string
}

// getAllResults ambil SEMUA result dari DB (seluruh riwayat)
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

// markovTransition hitung probabilitas transisi D2→D2 dari urutan result
// Menganalisa pola: setelah D2 X keluar, D2 apa yang paling sering muncul berikutnya
func markovTransition(entries []rawEntry) map[string]map[string]float64 {
	// trans[from][to] = count
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

	// Normalisasi menjadi probabilitas
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

// markovSesiTransition hitung transisi khusus antar sesi
// Menganalisa: sesi N → sesi N+1, digit apa yang sering muncul
func markovSesiTransition(entries []rawEntry, fromSesi, toSesi int) map[string]float64 {
	nextProb := map[string]float64{}
	counts := map[string]float64{}
	total := 0.0

	for i := 0; i < len(entries)-1; i++ {
		cur := entries[i]
		nxt := entries[i+1]
		// Pola sesi berurutan dalam sehari, atau sesi 6 → sesi 1 hari berikutnya
		isSesiTransition := (cur.sesi == fromSesi && nxt.sesi == toSesi)
		if !isSesiTransition {
			continue
		}
		if len(nxt.nomor) < 4 {
			continue
		}
		d2 := nxt.nomor[2:4]
		counts[d2]++
		total++
	}

	if total > 0 {
		for d2, c := range counts {
			nextProb[d2] = c / total
		}
	}
	return nextProb
}

// crossSesiPattern analisa pola lintas sesi: D2 yang sering muncul di sesi target
// berdasarkan data SEMUA sesi sebelumnya pada hari yang sama
func crossSesiPattern(entries []rawEntry, targetSesi int) map[string]float64 {
	scores := map[string]float64{}

	// Kelompokkan per tanggal
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

		// Analisa korelasi: digit dari sesi-sesi sebelumnya pada hari itu
		for s := 1; s < targetSesi; s++ {
			nomor, ok := sesiMap[s]
			if !ok || len(nomor) < 4 {
				continue
			}
			prevD2 := nomor[2:4]
			// Jika D2 sesi sebelumnya sama dengan target, ini pola repetisi
			if prevD2 == targetD2 {
				gap := targetSesi - s
				weight := 1.0 / float64(gap) // sesi lebih dekat → bobot lebih besar
				scores[targetD2] += weight
			}
		}
	}
	return scores
}

// periodikPattern cek apakah ada siklus periodik (tiap N putaran keluar)
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

	// Cek pola siklus 3, 4, 5, 6, 7 putaran
	for _, period := range []int{3, 4, 5, 6, 7} {
		if n < period*2 {
			continue
		}
		for i := period; i < n; i++ {
			if sesiOnly[i] == sesiOnly[i-period] {
				// Pola ditemukan — hitung jarak dari sekarang
				distFromEnd := n - 1 - i
				if distFromEnd < period {
					// Kandidat kuat: tepat di ujung siklus
					bonus := math.Max(0, float64(period-distFromEnd)) / float64(period)
					scores[sesiOnly[i]] += bonus * 2.0
				}
			}
		}
	}
	return scores
}

// analyzeD2Enhanced: analisa D2 lengkap dengan semua teknik
func analyzeD2Enhanced(targetSesi int) []D2Stat {
	now := nowWIB()
	allEntries := getAllResults()

	if len(allEntries) == 0 {
		return nil
	}

	// ── 1. Frekuensi dasar (semua sesi + sesi spesifik) dengan decay waktu ──
	type stat struct {
		allFreq     float64
		sesiFreq    float64
		lastIdx     int
		lastSesiIdx int
		markov      float64
	}
	stats := map[string]*stat{}

	ensure := func(d2 string) {
		if stats[d2] == nil {
			stats[d2] = &stat{lastIdx: 9999, lastSesiIdx: 9999}
		}
	}

	// Indeks semua entri untuk lastSeen
	allD2Sequence := []string{}
	for _, e := range allEntries {
		if len(e.nomor) >= 4 {
			allD2Sequence = append(allD2Sequence, e.nomor[2:4])
		}
	}

	sesiSequence := []string{}
	for _, e := range allEntries {
		if e.sesi == targetSesi && len(e.nomor) >= 4 {
			sesiSequence = append(sesiSequence, e.nomor[2:4])
		}
	}

	// Frekuensi semua sesi (bobot berdasarkan usia data)
	for i, e := range allEntries {
		if len(e.nomor) < 4 {
			continue
		}
		d2 := e.nomor[2:4]
		ensure(d2)
		t, err := time.Parse("2006-01-02", e.tanggal)
		weight := 1.0
		if err == nil {
			age := now.Sub(t).Hours() / 24
			// Bobot eksponensial: data baru jauh lebih penting
			weight = math.Exp(-age / 30.0) * 3.0
			if weight < 0.3 {
				weight = 0.3
			}
		}
		stats[d2].allFreq += weight
		if stats[d2].lastIdx == 9999 {
			stats[d2].lastIdx = len(allEntries) - i
		}
	}

	// Frekuensi sesi spesifik
	sesiEntries := []rawEntry{}
	for _, e := range allEntries {
		if e.sesi == targetSesi {
			sesiEntries = append(sesiEntries, e)
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
			weight = math.Exp(-age/20.0)*4.0 + 0.5
		}
		stats[d2].sesiFreq += weight
		if stats[d2].lastSesiIdx == 9999 {
			stats[d2].lastSesiIdx = len(sesiEntries) - i
		}
	}

	// ── 2. Markov Chain: transisi umum ──
	markovGen := markovTransition(allEntries)
	// Ambil D2 terakhir yang keluar
	lastD2 := ""
	if len(allD2Sequence) > 0 {
		lastD2 = allD2Sequence[len(allD2Sequence)-1]
	}
	if lastD2 != "" {
		if nextProbs, ok := markovGen[lastD2]; ok {
			for d2, prob := range nextProbs {
				ensure(d2)
				stats[d2].markov += prob * 5.0
			}
		}
	}

	// ── 3. Markov sesi: transisi sesi sebelumnya → sesi target ──
	prevSesi := targetSesi - 1
	if prevSesi < 1 {
		prevSesi = 6
	}
	sesiTransProb := markovSesiTransition(allEntries, prevSesi, targetSesi)
	for d2, prob := range sesiTransProb {
		ensure(d2)
		stats[d2].markov += prob * 6.0
	}

	// ── 4. Pola lintas sesi dalam sehari ──
	crossProb := crossSesiPattern(allEntries, targetSesi)
	for d2, score := range crossProb {
		ensure(d2)
		stats[d2].markov += score * 2.0
	}

	// ── 5. Pola periodik ──
	periodProb := periodikPattern(allEntries, targetSesi)
	for d2, score := range periodProb {
		ensure(d2)
		stats[d2].markov += score * 3.0
	}

	// ── 6. Due number bonus ──
	// D2 yang sudah lama tidak keluar di sesi ini mendapat bonus
	_ = sesiSequence

	// ── Gabungkan skor final ──
	var list []D2Stat
	for d2, s := range stats {
		dueBonus := 0.0
		if s.lastSesiIdx > 10 {
			// Semakin lama tidak keluar, semakin besar bonus
			dueBonus = math.Log(float64(s.lastSesiIdx)) * 1.5
		}

		score := s.sesiFreq*5.0 + s.allFreq*1.5 + s.markov + dueBonus

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

	sort.Slice(list, func(i, j int) bool {
		if list[i].Score != list[j].Score {
			return list[i].Score > list[j].Score
		}
		return list[i].D2 < list[j].D2
	})
	return list
}

// analyzeFreqItems untuk halaman paito
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

func buildAlasan(stats []D2Stat, sesi int) string {
	if len(stats) == 0 {
		return "Belum cukup data historis"
	}
	top := stats[0]

	var parts []string

	// Top 2D info
	parts = append(parts, fmt.Sprintf(
		"Sesi %d: 2D terkuat %s (skor %.0f, sesi %dx, markov %.1f)",
		sesi, top.D2, top.Score, top.SesiFreq, top.MarkovScore,
	))

	// Due numbers
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

func getCurrentPrediction(nextSesi int) *Prediction {
	today := nowWIB().Format("2006-01-02")

	var digits string
	db.QueryRow(
		`SELECT digits FROM bbfs_preds WHERE tanggal=? AND sesi=? AND source='AI-LOKAL'`,
		today, nextSesi).Scan(&digits)

	stats := analyzeD2Enhanced(nextSesi)

	if len(digits) < 5 {
		digits = buildBBFSFromStats(stats)
		if len(digits) >= 5 {
			savePrediction(today, nextSesi, digits, "AI-LOKAL")
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
		Alasan:   buildAlasan(top10, nextSesi),
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
		})
		return
	}

	tanggal := r.FormValue("tanggal")
	if tanggal == "" {
		tanggal = today
	}
	sesiTarget, _ := strconv.Atoi(r.FormValue("sesi"))
	if sesiTarget < 1 || sesiTarget > 6 {
		sesiTarget = next
	}

	paitoStats := analyzeD2Enhanced(sesiTarget)
	top10 := paitoStats
	if len(top10) > 10 {
		top10 = top10[:10]
	}
	bbfs := buildBBFSFromStats(paitoStats)

	pred := Prediction{
		Metode: "AI-LOKAL",
		Top2D:  top10,
		BBFS:   bbfs,
		Alasan: buildAlasan(top10, sesiTarget),
	}
	if len(bbfs) >= 5 {
		pred.BBFSList = strings.Split(bbfs, "")
		savePrediction(tanggal, sesiTarget, bbfs, "AI-LOKAL")
	}

	data := PageData{
		CurrentDate: tanggal,
		NextSesi:    sesiTarget,
		Predictions: []Prediction{pred},
	}
	if len(bbfs) >= 5 {
		data.BBFS = generateBBFS(bbfs)
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
	mux.HandleFunc("/api/results", apiResultsHandler)
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

	log.Println("🎰 Toto Macau 4D Predictor → http://0.0.0.0:5000")
	log.Fatal(http.ListenAndServe("0.0.0.0:5000", recovery(mux)))
}
