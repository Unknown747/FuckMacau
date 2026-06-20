---
name: BBFS-B Multi-set strategy
description: Cara buildBBFS2FromStats menghasilkan set digit alternatif dengan coverage berbeda dari BBFS-A.
---

## Strategi
- `buildBBFS2FromStats(stats, bbfsA)` menggunakan exhaustive C(10,6)=210 seperti BBFS-A.
- Pasang 2D yang KEDUANYA ada di BBFS-A → penalti ×0.1.
- Satu digit ada di BBFS-A → penalti ×0.6.
- Hasilnya: set digit yang overlap minimal dengan BBFS-A, sehingga bersama mencakup lebih banyak kombinasi.
- Disimpan ke DB dengan source='AI-LOKAL-2', berbeda dari BBFS-A ('AI-LOKAL').
- `calcWinRate`, `calcSesiStats`, `getBBFSValidations` hanya membaca source='AI-LOKAL' — tidak terpengaruh.

**Why:** Dua BBFS yang terlalu mirip tidak menambah coverage. Penalti memaksa BBFS-B memilih digit dari "zona berbeda".
