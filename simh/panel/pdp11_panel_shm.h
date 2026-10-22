/* pdp11_panel_shm.h: shared-memory layout of the PDP-11 front panel export

   Copyright (c) 2026, the pdp11-emu authors.  MIT licence (see LICENSE).

   This header is the canonical definition of the file that the patched
   Open SIMH PDP-11 simulator maps with "ATTACH PANEL <file>" and that
   pdp11d (server/internal/shm/layout.go mirrors these offsets) maps too.

   Byte order is little-endian, all fields are naturally aligned.  The file
   is exactly sizeof (PANEL_SHM) bytes.  On ATTACH the simulator opens or
   creates it (mode 0600, a regular file, never through a symbolic link; an
   existing file must belong to the simulator's user and grant no access to
   group or others), locks it with flock (one simulator per file), sizes it
   and writes the header.  If the file already holds a valid header of the
   same version, the bridge-owned fields (sr, sr_valid, halt_req,
   target_rate) and the counters (ring_wr, stop_count, trap_count,
   instr_count, dr_on, weight_sum; seq rounded up to even) are preserved, so
   a reader that keeps its mapping across simulator restarts sees them
   continue; anything else (another version too) is zeroed.  A reader must
   refuse any other version.

   Synchronisation
   ---------------
   ring:      single producer (simulator thread).  The producer writes
              ring[ring_wr % PANEL_RING] and then stores ring_wr + 1 with
              release semantics; a release fence before the entry's stores
              keeps them from becoming visible before the previous ring_wr.
              A consumer loads ring_wr with acquire semantics and reads
              entries [last, ring_wr).  If it falls more than PANEL_RING/2
              behind it must skip ahead (entries may be overwritten while
              being read); entry k is intact if ring_wr, loaded again after
              the copy, is below k + PANEL_RING.
   dr_on, weight_sum: updated with every sample, before its ring_wr store,
              each with a single relaxed 32-bit store.  A consumer takes
              differences (modulo 2^32) between two reads: bit i of the
              DISPLAY REGISTER was 1 for the fraction d(dr_on[i]) /
              d(weight_sum) of the time in between (the samples' DR, as
              time-weighted as the lamps of the samples themselves).  The
              17 words are not one snapshot: a sample stored while they are
              read can be in some of them only (the next read has it all).
   registers: seqlock.  The producer increments seq (odd = write in
              progress), writes the block, increments seq again.  A consumer
              retries while seq is odd or changed across its copy.
   bridge -> simulator words (sr, sr_valid, halt_req, target_rate): the
              bridge writes each with a single atomic 32-bit store.  Any
              halt_req != halt_ack is a request, so a writer that clears a
              word before writing it (Python's struct.pack_into does)
              causes spurious halts.  Both sides use explicit atomics
              (C11/GCC __atomic_*, Go sync/atomic) so the protocol is
              correct on weakly ordered hosts (arm64) too.

   Halt handshake (race free; halt_ack is written only by the simulator)
   --------------
   request:   while the CPU runs, the bridge stores halt_req = halt_ack + 1.
   honour:    whenever the simulator sees halt_req != halt_ack (at every
              sample, including the first one after entering sim_instr) it
              stores halt_ack = halt_req and stops with "Front panel HALT"
              before the next instruction is fetched.
   cancel:    after any other stop, and before writing any run command
              (cont, go, run, boot, step), the bridge stores
              halt_req = halt_ack, so a request that raced a natural stop
              cannot stop the next run.
   ATTACH sets halt_ack = halt_req.
*/

#ifndef PDP11_PANEL_SHM_H_
#define PDP11_PANEL_SHM_H_ 0

#include <stdint.h>
#include <stddef.h>

#define PANEL_MAGIC     0x50313150u     /* bytes 'P','1','1','P' */
#define PANEL_VERSION   3u
#define PANEL_RING      16384u          /* samples, power of 2 */

/* PANEL_SAMPLE.addr: physical address of the bus cycle plus flags */
#define PNL_PA_MASK     0x003FFFFFu     /* bits 21:0  physical address */
#define PNL_WR          (1u << 22)      /* cycle was a write (DATO/DATOB) */
#define PNL_BYTE        (1u << 23)      /* byte cycle */
#define PNL_IFETCH      (1u << 24)      /* instruction fetch */
#define PNL_V_MODE      25              /* bits 26:25 mode of the cycle: */
#define PNL_M_MODE      3u              /*   0=kernel 1=super 2=(illegal) 3=user */
#define PNL_WAIT        (1u << 27)      /* WAIT sample: pa = PA(PC), va = PC, data = R0 */
#define PNL_MMU         (1u << 28)      /* MMR0<0>: relocation enabled */
#define PNL_M22         (1u << 29)      /* MMR3<4>: 22-bit mapping */
#define PNL_DSPACE      (1u << 30)      /* cycle was a D-space reference */
#define PNL_IOPAGE      (1u << 31)      /* cycle addressed the I/O page */

/* PANEL_SHM.state */
#define PNL_ST_HALTED   0u              /* in SCP (CPU not executing) */
#define PNL_ST_RUNNING  1u              /* executing instructions */
#define PNL_ST_WAIT     2u              /* executing, CPU in WAIT state */

typedef struct {
    uint32_t addr;          /* PA<21:0> | PNL_* flags */
    uint16_t data;          /* data of the bus cycle (WAIT sample: R0) */
    uint16_t va;            /* virtual address<15:0> of the cycle (WAIT sample: PC) */
    uint32_t weight;        /* wall-clock microseconds the sample stands for (>= 1) */
    uint16_t pc;            /* PC (R7) when the sample was taken */
    uint16_t ir;            /* instruction register when the sample was taken */
    } PANEL_SAMPLE;         /* 16 bytes */

typedef struct {
    /* 0x000 identification, written by the simulator on ATTACH */
    uint32_t magic;             /*   0 */
    uint32_t version;           /*   4 */
    uint32_t size;              /*   8  sizeof (PANEL_SHM) */
    uint32_t ring_size;         /*  12  PANEL_RING */
    /* 0x010 simulator -> bridge, continuously updated */
    uint32_t ring_wr;           /*  16  total samples written (mod 2^32) */
    uint32_t state;             /*  20  PNL_ST_* */
    uint32_t seq;               /*  24  seqlock for the register block */
    uint32_t stop_count;        /*  28  ++ every time sim_instr returns */
    /* 0x020 register block (seqlock protected) */
    uint32_t r[8];              /*  32  R0-R5, SP (current mode), PC */
    uint32_t psw;               /*  64 */
    uint32_t dr;                /*  68  display register (17777570 writes) */
    uint32_t mmr0;              /*  72 */
    uint32_t mmr3;              /*  76 */
    uint32_t last_pa;           /*  80  PA + flags of the most recent bus cycle */
    uint32_t last_data;         /*  84  data of the most recent bus cycle */
    double   sim_time;          /*  88  sim_gtime () when the block was written */
    uint32_t sample_interval;   /*  96  current instructions between samples */
    uint32_t samples_per_sec;   /* 100  measured sample rate */
    /* 0x068 bridge -> simulator */
    uint32_t sr;                /* 104  switch register<15:0> as read at 17777570 */
    uint32_t sr_valid;          /* 108  nonzero: programs read sr */
    uint32_t halt_req;          /* 112  bridge increments to request a halt */
    uint32_t target_rate;       /* 116  desired samples/second (0 = 50000) */
    /* 0x078 simulator -> bridge */
    uint32_t trap_count;        /* 120  odd address / NXM / MMU abort / red stack count */
    uint32_t halt_ack;          /* 124  halt_req value last honoured */
    uint32_t pc_pa;             /* 128  PA of PC (relocated in the current mode), with the block */
    uint32_t pad0;              /* 132 */
    uint64_t instr_count;       /* 136  instructions executed (sum of sample intervals), with the block */
    /* 0x090 simulator -> bridge, with every sample (not in the block) */
    uint32_t dr_on[16];         /* 144  weight of the samples taken while DR bit i was 1 (us, mod 2^32) */
    uint32_t weight_sum;        /* 208  weight of all samples (us, mod 2^32) */
    uint32_t reserved[11];      /* 212 .. 255 */
    /* 0x100 */
    PANEL_SAMPLE ring[PANEL_RING];  /* 256 */
    } PANEL_SHM;                /* 256 + 16 * 16384 = 262400 bytes */

/* Layout checks (C11 only; SimH builds with -std=gnu99, so pdp11_panel.c
   repeats them as C89 typedef checks) */

#if defined(__STDC_VERSION__) && (__STDC_VERSION__ >= 201112L)
_Static_assert (sizeof (PANEL_SAMPLE) == 16, "PANEL_SAMPLE size");
_Static_assert (offsetof (PANEL_SHM, ring_wr) == 16, "ring_wr offset");
_Static_assert (offsetof (PANEL_SHM, r) == 32, "r offset");
_Static_assert (offsetof (PANEL_SHM, sim_time) == 88, "sim_time offset");
_Static_assert (offsetof (PANEL_SHM, sr) == 104, "sr offset");
_Static_assert (offsetof (PANEL_SHM, trap_count) == 120, "trap_count offset");
_Static_assert (offsetof (PANEL_SHM, pc_pa) == 128, "pc_pa offset");
_Static_assert (offsetof (PANEL_SHM, instr_count) == 136, "instr_count offset");
_Static_assert (offsetof (PANEL_SHM, dr_on) == 144, "dr_on offset");
_Static_assert (offsetof (PANEL_SHM, weight_sum) == 208, "weight_sum offset");
_Static_assert (offsetof (PANEL_SHM, ring) == 256, "ring offset");
_Static_assert (sizeof (PANEL_SHM) == 262400, "PANEL_SHM size");
#endif

#endif
