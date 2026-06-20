---
name: AB Correlation analysis
description: Fungsi abCorrelation() mencari CD yang sering muncul setelah pola AB di sesi sebelumnya.
---

## Cara kerja
1. Cari `lastAB` = 2 digit pertama (posisi AB) dari sesi `targetSesi - 1` paling baru.
2. Scan semua entry historis: jika sesi prev punya AB=lastAB, catat CD apa yang muncul di targetSesi hari yang sama.
3. Bobot: `exp(-age/35)*2.5 + 0.2` (entry lama tetap berkontribusi tapi lebih kecil).
4. Normalisasi ke 0-1, lalu diskor ke `stats[d2].corr += score * 3.5`.

**Why:** Pola AB→CD antar sesi berurutan merupakan "memory" dari dealer — jika AB kemarin adalah pola tertentu, CD berikutnya tidak acak sepenuhnya.

**How to apply:** Panggil setelah `digitPairCorrelation` dalam `analyzeD2Enhanced`, weight 3.5 sudah dikalibrasi bersama Markov.
