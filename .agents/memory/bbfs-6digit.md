---
name: BBFS 6-digit migration
description: Catatan migrasi BBFS dari 5 digit ke 6 digit dan cara handle entry DB lama.
---

## Aturan
- `buildBBFSFromStats` menggunakan exhaustive search C(10,6)=210 kombinasi, output selalu 6 digit.
- `generateBBFS` dan `isHit` tetap backward-compatible (cek `len(bbfs) < 5` bukan `< 6`).
- Saat `predictHandler` memuat BBFS dari DB, cek `len(pred.BBFS) >= 6`; jika < 6 (entry lama), force regenerate dan simpan ulang ke DB.

**Why:** DB memiliki entry lama 5-digit yang akan menampilkan hanya 5 kotak digit di UI, sementara label sudah bilang "6 Digit". Tanpa pengecekan ini, user akan melihat ketidakkonsistenan.

**How to apply:** Selalu tambahkan `&& len(pred.BBFS) >= 6` di kondisi "pakai entry DB" di predictHandler.
