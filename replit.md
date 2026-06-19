# Toto Macau 4D Predictor

Website prediksi Toto Macau 4D berbasis Golang dengan Gemini AI + analisa Paito, fokus pada prediksi 2D dan BBFS 5 digit.

## Tech Stack
- **Language**: Go 1.21
- **Database**: SQLite (`toto.db`) — tabel `results` saja
- **AI**: Google Gemini 1.5 Flash via REST API
- **Frontend**: HTML/CSS/JS (server-side rendering dengan Go templates)
- **Port**: 5000

## Fitur Utama
1. **Dashboard** — paito 14 hari + info sesi aktif
2. **Input Result** — manual input per sesi + import batch CSV
3. **Paito** — tabel warna historis + analisa frekuensi 2D
4. **Prediksi AI** — Gemini AI + analisa paito → TOP 2D + BBFS 5 digit
5. **BBFS Generator** — generate 20 kombinasi 2D dari 5 digit
6. **Filter Gemini** — upload beberapa kandidat BBFS, Gemini pilih terbaik untuk 6 sesi ke depan

## Database
Hanya 1 tabel utama:
```sql
results (id, tanggal, sesi 1-6, nomor 4D, created_at)
```
Tabel lama (`predictions`, `tune_history`) sudah dihapus.

## Cara Jalankan
```bash
go run .
```

## Gemini API Key
Masukkan di halaman Prediksi AI atau set environment variable:
```
GEMINI_API_KEY=AIzaSy...
```
Gratis di https://aistudio.google.com/app/apikey

## User Preferences
- Bahasa Indonesia untuk UI
- Fokus prediksi 2D (bukan 4D penuh)
- Macau 6 sesi per hari
