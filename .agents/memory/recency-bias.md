---
name: Recency bias reduction
description: Weight formula untuk allFreq dan sesiFreq di analyzeD2Enhanced.
---

## Formula saat ini (post-fix)
- **allFreq** age≤7: `4.0 - age*0.1` (sebelumnya 8.0 - age*0.5)
- **allFreq** age≤30: `exp(-age/18)*2.5 + 0.3`
- **allFreq** age>30: `exp(-age/50)*1.2 + 0.2`
- **sesiFreq** age≤7: `5.5 - age*0.2` (sebelumnya 10.0 - age*0.6)
- **sesiFreq** age≤21: `exp(-age/12)*3.5 + 0.4`
- **sesiFreq** age>21: `exp(-age/35)*2.0 + 0.2`

**Why:** Bias ekstrim terhadap 7 hari terakhir menyebabkan prediksi terlalu dipengaruhi hasil sangat baru, mengabaikan pola jangka menengah (8-30 hari).
