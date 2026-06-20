---
name: Removed AI components
description: Gap/Overdue dan DOW dihapus dari sistem prediksi karena tidak valid secara statistik.
---

## Komponen yang dihapus

### Gap/Overdue (`gapAnalysis`)
- **Alasan dihapus:** Gambler's fallacy — angka acak tidak menjadi "due" karena lama tidak muncul. Setiap draw independen.
- **Implementasi sebelumnya:** gapAnalysis() membandingkan gap sekarang vs rata-rata historis, memberi bonus besar jika sangat overdue.
- **Bahaya:** Bisa aktif menyesatkan prediksi dengan memilih angka yang justru tidak punya sinyal kuat.

### DOW/Day-of-Week (`dowPattern`)
- **Alasan dihapus:** Noise, bukan sinyal nyata. Dengan dataset < 1 tahun, setiap kombinasi sesi×hari hanya punya ~5-10 sampel — terlalu kecil untuk pola bermakna.

## Lapisan yang diupdate saat penghapusan
1. `D2Stat` struct — hapus `GapScore`, `DowScore`
2. `LearnedWeights` struct — hapus `GapMult`, `DowMult`
3. `globalWeights` default — hapus inisialisasi Gap/Dow
4. `loadLearnedWeights()` — hapus kolom gap_mult/dow_mult dari SELECT
5. `retuneWeights()` — hapus stats["gap"]/["dow"], update query dan UPDATE
6. `getCompStats()` — hapus "Gap"/"DOW" dari stats map, query, order
7. `savePredComponent()` — hapus gap_score/dow_score dari INSERT
8. `analyzeD2Enhanced()` — hapus inner stat fields gap/dow, seenD2 block, dow section, scoring terms
9. `predict.html` transparency table — hapus kolom Gap dan DOW

**Why:** Komponen jelek bisa aktif merusak ranking — lebih baik tidak ada daripada ada tapi misleading.
