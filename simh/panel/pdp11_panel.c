/* pdp11_panel.c: PDP-11 front panel export (shared memory file)

   Copyright (c) 2026, the pdp11-emu authors

   Permission is hereby granted, free of charge, to any person obtaining a
   copy of this software and associated documentation files (the "Software"),
   to deal in the Software without restriction, including without limitation
   the rights to use, copy, modify, merge, publish, distribute, sublicense,
   and/or sell copies of the Software, and to permit persons to whom the
   Software is furnished to do so, subject to the following conditions:

   The above copyright notice and this permission notice shall be included in
   all copies or substantial portions of the Software.

   THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
   IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
   FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.  IN NO EVENT SHALL
   THE AUTHORS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER
   IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN
   CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.

   panel        front panel export

   The PANEL device maps the file named by ATTACH PANEL <file> (normally on a
   tmpfs) that an external program (pdp11d) maps too.  The layout and the
   protocol are defined in pdp11_panel_shm.h; this module is the only writer
   of the simulator-owned words.

   ATTACH PANEL <file>  open or create the file (a regular file, not a symbolic
                        link, owned by this user and closed to group and
                        others), lock it (flock: one simulator per file), size
                        it, map it shared.  A file that holds a version 3
                        header keeps the bridge words (sr, sr_valid, halt_req,
                        target_rate) and the counters; anything else is
                        zeroed.  halt_ack = halt_req.
   DETACH PANEL         unmap; every hook becomes a no-op
   SET PANEL REFRESH    rewrite the register block now (pdp11d uses it while
                        the CPU is halted, after DEPOSIT and RESET)
   SHOW PANEL           file, sample rate, interval, samples, state

   Sampling.  sim_instr decrements a countdown (a local, so that it can live
   in a register) once per instruction, and panel_wcountdown in every WAIT
   state iteration.  When one of them expires, or when sim_idle slept in the
   WAIT state, one sample goes to the ring: the most recent bus cycle of the
   last instruction (instruction sample) or PA(PC), PC and R0 (WAIT sample),
   weighted with the wall-clock time it stands for (panel_weight: the work
   since the previous sample at the measured wall time per step, so that a
   throttled CPU's sleeps do not all fall on single samples).  An abort
   (longjmp out of an instruction) loses the local countdown; it restarts at
   a random point of its period (panel_rearm).  The DISPLAY REGISTER is
   accumulated per bit with the weight of every sample (dr_on).

   The instruction fetch carries no hook: the CPU clears panel_bus once per
   instruction and only data references store into it.  If it is still clear
   at a sample, the last bus cycle was the fetch of the instruction at
   inst_pc, which is rebuilt here (relocation in the mode the instruction
   began in, memory read); the IR of a sample is rebuilt the same way.  A
   data cycle has the mode of its virtual address (MTPI/MTPD write in the
   previous mode, RTI/RTT pop in the old one).

   Rate.  At least every 100 ms of run time the interval is set so that
   samples/sec is about target_rate (default 50000), earlier when the rate is
   more than twice too high (a busy period after an idle one); every reload
   is dithered by +-12.5% with an LFSR so that short loops do not alias with
   the interval.  WAIT iterations have their own countdown so that
   instr_count counts instructions only.  With idling, sim_idle sleeps whole
   milliseconds and spins out the rest of a tick; that spin is not sampled
   (PNL_SPIN_SKIP), so an idle CPU gives one sleep sample per wake-up, plus
   light busy-wait samples where SimH spins longer or does not sleep.

   Register block (seqlock): every 64 samples, at every WAIT sample after a
   sleep, when the interval changes, at sim_instr exit, on ATTACH and on SET
   PANEL REFRESH.

   Halt handshake.  Every sample compares halt_req with halt_ack.  When they
   differ the simulator stores halt_ack = halt_req and stops with STOP_PANEL
   ("Front panel HALT") before the next instruction is fetched (or before
   sim_idle could sleep).  sim_instr entry sets both countdowns to 1 (also
   after an abort before the first sample), so a request that is pending
   when the CPU starts is honoured at once; a request is never discarded.

   Hosts without mmap (Windows, VMS) get stubs: ATTACH fails, the hooks are
   no-ops.
*/

#include "pdp11_defs.h"
#include "pdp11_panel.h"

#if defined (_WIN32) || defined (VMS) || defined (PANEL_NO_SHM)
#define PANEL_HAVE_SHM  0
#else
#define PANEL_HAVE_SHM  1
#include <errno.h>
#include <fcntl.h>
#include <sys/file.h>
#include <sys/mman.h>
#include <sys/stat.h>
#include <time.h>
#include <unistd.h>
#endif

#define PNL_RATE_DEF    50000                           /* target_rate == 0 */
#define PNL_INT_MIN     1                               /* sample interval limits */
#define PNL_INT_MAX     10000000
#define PNL_INT_INIT    1000                            /* interval after ATTACH */
#define PNL_ADAPT_NS    100000000                       /* adaptation period (ns) */
#define PNL_RESID_NS    4000000                         /* weight residual limit (ns) */
#define PNL_REG_SMP     64                              /* register refresh (samples) */
#define PNL_SPIN_SKIP   0x20000                         /* WAIT iterations after a sleep */
#define PNL_IR_WAIT     0000001                         /* IR of a WAIT sample */
#define PNL_LFSR_TAPS   0x80200003u                     /* 32b maximal length */

/* Layout checks that also work before C11 (the header's _Static_asserts
   are compiled only under C11; SimH builds with -std=gnu99) */

#define PNL_CHECK(nm,c) typedef char pnl_check_##nm[(c)? 1: -1]
PNL_CHECK (sample, sizeof (PANEL_SAMPLE) == 16);
PNL_CHECK (ring_wr, offsetof (PANEL_SHM, ring_wr) == 16);
PNL_CHECK (r, offsetof (PANEL_SHM, r) == 32);
PNL_CHECK (sim_time, offsetof (PANEL_SHM, sim_time) == 88);
PNL_CHECK (sr, offsetof (PANEL_SHM, sr) == 104);
PNL_CHECK (trap_count, offsetof (PANEL_SHM, trap_count) == 120);
PNL_CHECK (pc_pa, offsetof (PANEL_SHM, pc_pa) == 128);
PNL_CHECK (instr_count, offsetof (PANEL_SHM, instr_count) == 136);
PNL_CHECK (dr_on, offsetof (PANEL_SHM, dr_on) == 144);
PNL_CHECK (weight_sum, offsetof (PANEL_SHM, weight_sum) == 208);
PNL_CHECK (ring, offsetof (PANEL_SHM, ring) == 256);
PNL_CHECK (size, sizeof (PANEL_SHM) == 262400);

/* Hook state, used by pdp11_cpu.c */

PANEL_SHM *panel_shm = NULL;                            /* mapped file */
t_uint64 panel_bus = 0;                                 /* last data cycle */
uint32 panel_bus_va = 0;                                /* last data VA */
int32 panel_wcountdown = PANEL_CD_NEVER;

/* Private state */

static int32 panel_interval = PNL_INT_INIT;             /* adaptive interval */
static uint32 panel_samples = 0;                        /* samples since ATTACH */
static uint32 panel_sps = 0;                            /* measured samples/sec */

t_stat panel_attach (UNIT *uptr, CONST char *cptr);
t_stat panel_detach (UNIT *uptr);
t_stat panel_set_refresh (UNIT *uptr, int32 val, CONST char *cptr, void *desc);
t_stat panel_show_status (FILE *st, UNIT *uptr, int32 val, CONST void *desc);
t_stat panel_help (FILE *st, DEVICE *dptr, UNIT *uptr, int32 flag, const char *cptr);
const char *panel_description (DEVICE *dptr);

/* PANEL data structures

   panel_dev    PANEL device descriptor
   panel_unit   PANEL unit descriptor
   panel_reg    PANEL register list
   panel_mod    PANEL modifier list
*/

UNIT panel_unit = { UDATA (NULL, UNIT_ATTABLE, 0) };

REG panel_reg[] = {
    { DRDATAD (INTERVAL, panel_interval, 32, "instructions between samples (adaptive)"), REG_RO },
    { DRDATAD (SAMPLES, panel_samples, 32, "samples written since ATTACH"), REG_RO },
    { DRDATAD (RATE, panel_sps, 32, "measured samples per second"), REG_RO },
    { NULL }
    };

MTAB panel_mod[] = {
    { MTAB_XTD|MTAB_VDV, 0, "STATUS", NULL,
      NULL, &panel_show_status, NULL, "Display the export status" },
    { MTAB_XTD|MTAB_VDV, 0, NULL, "REFRESH",
      &panel_set_refresh, NULL, NULL, "Rewrite the register block now" },
    { 0 }
    };

DEVICE panel_dev = {
    "PANEL", &panel_unit, panel_reg, panel_mod,
    1, 10, 31, 1, 8, 16,
    NULL, NULL, NULL,
    NULL, &panel_attach, &panel_detach,
    NULL, DEV_NOSAVE, 0,
    NULL, NULL, NULL,
    &panel_help, NULL, NULL,
    &panel_description
    };

#if PANEL_HAVE_SHM

extern int32 R[8], REGFILE[6][2], STACKFILE[4], saved_PC;
extern int16 inst_pc;
extern int32 inst_psw, PSW, cm, MMR0, MMR3, DR;
extern t_stat reason;
int32 get_PSW (void);
int32 relocC (int32 va, int32 sw);

#define PNL_STORE(f,v)  __atomic_store_n (&(f), (v), __ATOMIC_RELAXED)
#define PNL_PUBLISH(f,v) __atomic_store_n (&(f), (v), __ATOMIC_RELEASE)

static int panel_fd = -1;                               /* open, locked while attached */
static int32 panel_period = 0;                          /* instr countdown reload */
static int32 panel_wperiod = 0;                         /* WAIT countdown reload */
static uint32 panel_lfsr = 0x2545F491u;                 /* dither LFSR */
static uint32 panel_xs = 0x9E3779B9u;                   /* abort restart xorshift */
static t_bool panel_running = FALSE;                    /* inside sim_instr */
static uint32 panel_ring_wr = 0;                        /* copies of the words */
static uint32 panel_seq = 0;                            /* only the simulator */
static uint32 panel_state = PNL_ST_HALTED;              /* writes */
static uint32 panel_stops = 0;
static uint32 panel_traps = 0;
static uint32 panel_halt_ack = 0;
static t_uint64 panel_icount = 0;
static uint32 panel_dr_on[16];
static uint32 panel_wsum = 0;
static t_uint64 panel_t_last = 0;                       /* weight baseline (ns) */
static t_uint64 panel_t_smp = 0;                        /* previous sample (ns) */
static double panel_cpc = 0.0;                          /* ns per step, 0: none yet */
static t_uint64 panel_t_adapt = 0;                      /* adaptation window start */
static t_uint64 panel_t_exit = 0;                       /* last sim_instr exit */
static uint32 panel_win_smp = 0;                        /* window: samples */
static t_uint64 panel_win_cnt = 0;                      /* window: countdown steps */
static t_uint64 panel_win_wall = 0;                     /* window: wall time (ns) and */
static t_uint64 panel_win_work = 0;                     /* steps of the work samples */

/* Monotonic wall clock in nanoseconds */

static t_uint64 panel_now (void)
{
struct timespec ts;

clock_gettime (CLOCK_MONOTONIC, &ts);
return ((t_uint64) ts.tv_sec * 1000000000u) + (t_uint64) ts.tv_nsec;
}

/* Weight residual: the wall time (ns) not given to a sample yet; negative
   when the weights are ahead of the clock */

static t_int64 panel_resid (t_uint64 now)
{
return (now >= panel_t_last)? (t_int64) (now - panel_t_last):
    -(t_int64) (panel_t_last - now);
}

/* Weight of a sample in whole microseconds, at least 1.  panel_t_last
   advances by the weights given, so that the weights of a run add up to its
   wall time.

   A sample that settles (the one after an idle sleep, or the last of a run:
   a halt request) gets the whole residual: the wall time since the previous
   sample plus whatever earlier samples left.  Any other sample stands for
   the work since the previous sample of its kind, its countdown steps
   (instructions or WAIT iterations), at panel_cpc, the wall time per step
   of the previous adaptation window.  Throttled, SimH executes about 1000
   instructions at host speed and then sleeps a millisecond: weighted with
   the wall time since the previous sample, the first sample after a sleep
   would get the whole millisecond and the others nothing, and a partly lit
   lamp would show the noise of ~17 samples per frame instead of ~830.  The
   residual stays within +-PNL_RESID_NS (a sample that would leave more gets
   the difference, and before the first adaptation every sample gets the
   residual), and panel_adapt pays it back over the next window.  The
   microsecond given to a sample that stands for less is borrowed from the
   following ones, up to PNL_RESID_NS ahead (beyond that it counts extra). */

static uint32 panel_weight (t_uint64 now, uint32 steps, t_bool settle)
{
t_int64 resid = panel_resid (now);
t_int64 w = resid;
double x;

if (!settle) {
    panel_win_wall = panel_win_wall + (now - panel_t_smp);
    panel_win_work = panel_win_work + steps;
    if (panel_cpc > 0.0) {                              /* the work done */
        x = (double) steps * panel_cpc;
        if (x < (double) (resid - PNL_RESID_NS))
            w = resid - PNL_RESID_NS;
        else if (x > (double) (resid + PNL_RESID_NS))
            w = resid + PNL_RESID_NS;
        else w = (t_int64) x;
        }
    }
panel_t_smp = now;
w = (w + 500) / 1000;                                   /* whole us */
if (w < 1)
    w = 1;
panel_t_last = panel_t_last + ((t_uint64) w * 1000);
if ((panel_t_last > now) && ((panel_t_last - now) > PNL_RESID_NS))
    panel_t_last = now + PNL_RESID_NS;                  /* borrow at most that */
return (w > 0xFFFFFFFF)? 0xFFFFFFFFu: (uint32) w;
}

static uint32 panel_target (void)
{
uint32 t = __atomic_load_n (&panel_shm->target_rate, __ATOMIC_RELAXED);

return (t != 0)? t: PNL_RATE_DEF;
}

/* Mode and mapping flags of a sample */

static uint32 panel_mflags (int32 mode)
{
return (((uint32) mode & PNL_M_MODE) << PNL_V_MODE) |
    ((MMR0 & MMR0_MME)? PNL_MMU: 0) |
    ((MMR3 & MMR3_M22E)? PNL_M22: 0);
}


/* Physical address of an instruction address in the given mode (I space);
   -1 if it is not mapped */

static int32 panel_reloc (int32 pc, int32 mode)
{
int32 pa = relocC (pc & 0177777, (mode == MD_KER)? SWMASK ('K'):
    ((mode == MD_SUP)? SWMASK ('S'): SWMASK ('U')));

return ((pa < 0) || (pa >= MAXMEMSIZE))? -1: pa;
}

/* PA of PC as exported: the most recent data address if PC is not mapped */

static uint32 panel_pc_pa (int32 pc, int32 mode)
{
int32 pa = panel_reloc (pc, mode);

return (pa < 0)? ((uint32) panel_bus & PNL_PA_MASK): (uint32) pa;
}

/* The most recent bus cycle of the last instruction: its last data cycle,
   in the mode of that cycle (the mode bits of its virtual address: MTPI and
   MTPD write in the previous mode, RTI and RTT pop in the old one), or (none
   since its fetch) the fetch rebuilt from inst_pc in the mode the
   instruction began in.  *ir gets the instruction at inst_pc the same way
   (from memory: code in the I/O page reads as 0). */

static uint32 panel_last_cycle (int32 *data, int32 *va, int32 *ir)
{
uint32 a = (uint32) panel_bus;
int32 imode = (inst_psw >> PSW_V_CM) & 03;
int32 ipa = panel_reloc (inst_pc, imode);
uint32 pa = (ipa < 0)? (a & PNL_PA_MASK): (uint32) ipa;

*ir = ((ipa >= 0) && ADDR_IS_MEM (ipa))? RdMemW (ipa): 0;
if (a & PANEL_CYCLE) {                                  /* data cycle */
    a = a & ~PANEL_CYCLE;
    if (panel_bus_va & VA_DS)
        a = a | PNL_DSPACE;
    *data = (int32) ((panel_bus >> 32) & 0177777);
    *va = (int32) (panel_bus_va & 0177777);
    return a | panel_mflags ((int32) (panel_bus_va >> VA_V_MODE) & 03);
    }
*data = *ir;                                            /* instruction fetch */
*va = inst_pc & 0177777;
return pa | PNL_IFETCH | ((pa >= IOPAGEBASE)? PNL_IOPAGE: 0) | panel_mflags (imode);
}

/* Next countdown: the interval +-12.5% */

static int32 panel_reload (void)
{
int32 n = panel_interval;
int32 span = n >> 2;

panel_lfsr = (panel_lfsr >> 1) ^ ((0u - (panel_lfsr & 1u)) & PNL_LFSR_TAPS);
if (span > 0)
    n = n - (n >> 3) + (int32) (panel_lfsr % (uint32) (span + 1));
return n;
}

/* Rewrite the register block (seqlock: odd while writing).  Inside sim_instr
   the working registers are current; while halted it is the state SCP sees
   (EXAMINE/DEPOSIT operate on saved_PC, REGFILE, STACKFILE and PSW). */

static void panel_regs (void)
{
PANEL_SHM *p = panel_shm;
int32 r[8], psw, mode, i, data, va, ir;
uint32 pc_pa, cycle;
double t = sim_gtime ();

if (panel_running) {
    for (i = 0; i < 8; i++)
        r[i] = R[i];
    psw = get_PSW ();
    mode = cm;
    }
else {
    int32 rset = (PSW >> PSW_V_RS) & 01;

    psw = PSW;
    mode = (PSW >> PSW_V_CM) & 03;
    for (i = 0; i < 6; i++)
        r[i] = REGFILE[i][rset];
    r[6] = STACKFILE[mode];
    r[7] = saved_PC;
    }
pc_pa = panel_pc_pa (r[7], mode);
cycle = panel_last_cycle (&data, &va, &ir);
panel_seq = panel_seq + 1;                              /* odd: writing */
PNL_PUBLISH (p->seq, panel_seq);
__atomic_thread_fence (__ATOMIC_RELEASE);
for (i = 0; i < 8; i++)
    PNL_STORE (p->r[i], (uint32) r[i] & 0177777);
PNL_STORE (p->psw, (uint32) psw & 0177777);
PNL_STORE (p->dr, (uint32) DR & 0177777);
PNL_STORE (p->mmr0, (uint32) MMR0 & 0177777);
PNL_STORE (p->mmr3, (uint32) MMR3 & 0177777);
PNL_STORE (p->last_pa, cycle);
PNL_STORE (p->last_data, (uint32) data);
__atomic_store (&p->sim_time, &t, __ATOMIC_RELAXED);
PNL_STORE (p->sample_interval, (uint32) panel_interval);
PNL_STORE (p->samples_per_sec, panel_sps);
PNL_STORE (p->pc_pa, pc_pa);
PNL_STORE (p->instr_count, panel_icount);
panel_seq = panel_seq + 1;                              /* even: stable */
PNL_PUBLISH (p->seq, panel_seq);
}

static void panel_set_state (uint32 st)
{
if (panel_state != st) {
    panel_state = st;
    PNL_PUBLISH (panel_shm->state, st);
    }
}

/* Append one sample to the ring and add its weight to the DISPLAY REGISTER
   accumulators.  The fence keeps the entry's stores from becoming visible
   before the previous ring_wr (a release store orders only what precedes
   it): a reader that finds a slot's new contents then also finds that the
   slot was overwritten. */

static void panel_put (uint32 addr, int32 data, int32 va, uint32 weight, int32 ir)
{
PANEL_SHM *p = panel_shm;
PANEL_SAMPLE *sp = &p->ring[panel_ring_wr & (PANEL_RING - 1)];
uint32 dr = (uint32) DR & 0177777;
int32 i;

__atomic_thread_fence (__ATOMIC_RELEASE);
sp->addr = addr;
sp->data = (uint16_t) data;
sp->va = (uint16_t) va;
sp->weight = weight;
sp->pc = (uint16_t) PC;
sp->ir = (uint16_t) ir;
for (i = 0; dr != 0; i++, dr = dr >> 1) {
    if (dr & 1) {
        panel_dr_on[i] = panel_dr_on[i] + weight;
        PNL_STORE (p->dr_on[i], panel_dr_on[i]);
        }
    }
panel_wsum = panel_wsum + weight;
PNL_STORE (p->weight_sum, panel_wsum);
panel_ring_wr = panel_ring_wr + 1;
PNL_PUBLISH (p->ring_wr, panel_ring_wr);
panel_samples = panel_samples + 1;
panel_win_smp = panel_win_smp + 1;
}

/* Honour a halt request: TRUE if the CPU must stop */

static t_bool panel_halt (void)
{
uint32 req = __atomic_load_n (&panel_shm->halt_req, __ATOMIC_ACQUIRE);

if (req == panel_halt_ack)
    return FALSE;
panel_halt_ack = req;
PNL_PUBLISH (panel_shm->halt_ack, req);
reason = STOP_PANEL;
return TRUE;
}

/* Adapt the interval to the measured rate.  The window normally spans 100 ms
   of run time; it ends early once it holds twice the samples a full window
   should, so that a burst after an idle period (interval near 1) is cut
   short before it floods the ring.  The window also measures the weight per
   step (the wall time of the samples that weigh their work, idle sleeps
   excluded), raised or lowered so that the residual is paid back over the
   next PNL_ADAPT_NS.  TRUE when the interval was set. */

static t_bool panel_adapt (t_uint64 now)
{
t_uint64 dt = now - panel_t_adapt;
uint32 target = panel_target ();
double n;

if (((dt < PNL_ADAPT_NS) &&
     (panel_win_smp < ((target / 5) + PNL_REG_SMP))) ||
    (dt == 0))
    return FALSE;
panel_sps = (uint32) (((double) panel_win_smp * 1e9) / (double) dt + 0.5);
n = ((double) panel_win_cnt * 1e9) / ((double) dt * (double) target);
if (n < PNL_INT_MIN)
    n = PNL_INT_MIN;
if (n > PNL_INT_MAX)
    n = PNL_INT_MAX;
panel_interval = (int32) (n + 0.5);
if (panel_win_work != 0)
    panel_cpc = ((double) panel_win_wall / (double) panel_win_work) *
        (1.0 + (double) panel_resid (now) / PNL_ADAPT_NS);
panel_t_adapt = now;
panel_win_smp = 0;
panel_win_cnt = 0;
panel_win_wall = 0;
panel_win_work = 0;
return TRUE;
}

/* Instruction sampler: called between instructions when the countdown
   expired.  Records the last bus cycle and returns the next countdown.  On a
   halt request it sets reason = STOP_PANEL; the CPU stops before the fetch. */

int32 panel_sample (void)
{
t_uint64 now;
t_bool regs, stop;
uint32 addr;
int32 data, va, ir;

if (panel_shm == NULL)                                  /* not attached? */
    return PANEL_CD_NEVER;
now = panel_now ();
panel_icount = panel_icount + (uint32) panel_period;    /* instructions since */
panel_win_cnt = panel_win_cnt + (uint32) panel_period;  /* the previous sample */
stop = panel_halt ();                                   /* sets reason */
addr = panel_last_cycle (&data, &va, &ir);
panel_put (addr, data, va, panel_weight (now, (uint32) panel_period, stop), ir);
panel_set_state (PNL_ST_RUNNING);
regs = panel_adapt (now) || ((panel_samples % PNL_REG_SMP) == 0);
if (stop) {                                             /* halt requested? */
    panel_period = 0;                                   /* the next instruction */
    return 1;                                           /* is not executed; */
    }                                                   /* exit writes the regs */
if (panel_wcountdown > 1) {                             /* sample the next WAIT */
    panel_wperiod = panel_wperiod - panel_wcountdown + 1; /* iteration, before */
    panel_wcountdown = 1;                               /* sim_idle sleeps */
    }
panel_period = panel_reload ();
if (regs)
    panel_regs ();
return panel_period;
}

/* WAIT sampler: called in the WAIT state when panel_wcountdown expired
   (before sim_idle; busy wait with idling off or throttled, and the first
   WAIT iteration after an instruction sample) or when sim_idle slept.
   sim_idle sleeps whole milliseconds and spins out the rest of a tick; after
   a sleep the countdown is at least PNL_SPIN_SKIP, so that this spin is not
   sampled: with idling there is one sleep sample per idle wake-up (it carries
   the time slept), and the next WAIT sample the work of the spin.
   The register block is rewritten at every sample after a sleep and every 64
   samples while the CPU busy-waits.  TRUE: halt requested (the caller must
   not idle). */

t_bool panel_wait (t_bool slept)
{
t_uint64 now;
uint32 pa, steps = 0;
t_bool regs, stop;

if (panel_shm == NULL) {                                /* not attached? */
    panel_wcountdown = PANEL_CD_NEVER;
    return FALSE;
    }
now = panel_now ();
if (panel_wperiod > panel_wcountdown)                   /* WAIT iterations since */
    steps = (uint32) (panel_wperiod - panel_wcountdown);/* the previous sample */
panel_win_cnt = panel_win_cnt + steps;
stop = panel_halt ();                                   /* sets reason */
pa = panel_pc_pa (PC, cm);
panel_put (pa | PNL_WAIT | panel_mflags (cm) | ((pa >= IOPAGEBASE)? PNL_IOPAGE: 0),
    R[0], PC, panel_weight (now, steps, slept || stop), PNL_IR_WAIT);
panel_set_state (PNL_ST_WAIT);
regs = panel_adapt (now) || slept || ((panel_samples % PNL_REG_SMP) == 0);
panel_wperiod = panel_reload ();
if (slept && (panel_wperiod < PNL_SPIN_SKIP))
    panel_wperiod = PNL_SPIN_SKIP;
panel_wcountdown = panel_wperiod;
if (regs)
    panel_regs ();
return stop;
}

/* sim_instr entry: sample at once (a halt request may be pending); the
   instruction countdown starts at 1 (panel_rearm) */

void panel_enter (void)
{
t_uint64 now;

if (panel_shm == NULL) {                                /* not attached? */
    panel_wcountdown = PANEL_CD_NEVER;
    return;
    }
now = panel_now ();
panel_running = TRUE;
panel_period = 0;
panel_wperiod = 0;
panel_wcountdown = 1;
panel_t_last = now;                                     /* halted time: no weight */
panel_t_smp = now;
panel_t_adapt = panel_t_adapt + (now - panel_t_exit);   /* and not in the rate */
panel_set_state (PNL_ST_RUNNING);
}

/* Instruction countdown when sim_instr enters its main loop: at entry, and
   after every abort (the setjmp return path), which loses panel_cd (a local
   changed after setjmp).  Before the first sample of a run it is 1, so a
   pending halt request is honoured at once.  After that it restarts at a
   random point of the current period: at a random moment the countdown is
   about evenly spread over the period, so the samples stay a random subset
   at the adaptive rate and the next sample's period keeps instr_count
   unbiased (exact only for runs without aborts).  Restarting at 1 forced a
   sample at the trap sequence of every abort; restarting with a whole period
   would never expire while aborts come more often than that.  The value
   comes from a xorshift generator of its own: successive states of the
   dither LFSR are shifted copies of each other, and drawing from it here
   (with the reloads taking values in between) lowered the sample rate of an
   abort loop, and with it instr_count, by a quarter. */

int32 panel_rearm (void)
{
if (panel_shm == NULL)                                  /* not attached? */
    return PANEL_CD_NEVER;
if (panel_period <= 0)                                  /* no sample yet */
    return 1;
panel_xs = panel_xs ^ (panel_xs << 13);
panel_xs = panel_xs ^ (panel_xs >> 17);
panel_xs = panel_xs ^ (panel_xs << 5);
return 1 + (int32) (panel_xs % (uint32) panel_period);
}

/* sim_instr exit (after the registers were saved); cd = the countdown */

void panel_exit (int32 cd)
{
int32 done;

if (panel_shm == NULL)
    return;
done = panel_period - cd + 1;                           /* instructions since */
if (done > 0)                                           /* the last sample */
    panel_icount = panel_icount + (uint32) done;
panel_regs ();
panel_running = FALSE;
panel_set_state (PNL_ST_HALTED);
panel_stops = panel_stops + 1;
PNL_PUBLISH (panel_shm->stop_count, panel_stops);
panel_t_exit = panel_now ();
}

/* Red stack, odd address, NXM or MMU trap dispatched (ADRS ERR lamp) */

void panel_trap (void)
{
if (panel_shm == NULL)
    return;
panel_traps = panel_traps + 1;
PNL_PUBLISH (panel_shm->trap_count, panel_traps);
}

/* Switch register read: the bridge's value when it marked it valid */

int32 panel_sr (int32 sr)
{
if ((panel_shm != NULL) &&
    (__atomic_load_n (&panel_shm->sr_valid, __ATOMIC_ACQUIRE) != 0))
    return (int32) (__atomic_load_n (&panel_shm->sr, __ATOMIC_RELAXED) & 0177777);
return sr;
}

/* Attach: map the file, keep the bridge words of a valid version 3 file.
   The file is reused (the counters continue), so an existing one must be
   this user's and closed to everybody else: another local user who created
   it first (in a world-writable directory such as /dev/shm) could otherwise
   map it too, read the bus samples (typed characters among them) and write
   halt requests and switch settings. */

t_stat panel_attach (UNIT *uptr, CONST char *cptr)
{
PANEL_SHM *p;
struct stat st;
char *fname;
int fd, err, i, oflags = O_RDWR | O_CREAT;
t_uint64 now;

if ((cptr == NULL) || (*cptr == 0))
    return SCPE_2FARG;
if (uptr->flags & UNIT_ATT)
    panel_detach (uptr);
fname = (char *) calloc (CBUFSIZE, sizeof (char));
if (fname == NULL)
    return SCPE_MEM;
strlcpy (fname, cptr, CBUFSIZE);
#if defined (O_NOFOLLOW)
oflags = oflags | O_NOFOLLOW;                           /* no symlinks */
#endif
#if defined (O_CLOEXEC)
oflags = oflags | O_CLOEXEC;
#endif
fd = open (fname, oflags, 0600);
if (fd < 0) {
    err = errno;
    free (fname);
    return sim_messagef (SCPE_OPENERR, "PANEL: can't open %s: %s\n", cptr, strerror (err));
    }
if ((fstat (fd, &st) != 0) || !S_ISREG (st.st_mode)) {
    close (fd);
    free (fname);
    return sim_messagef (SCPE_OPENERR, "PANEL: %s is not a regular file\n", cptr);
    }
if (st.st_uid != geteuid ()) {                          /* someone else's? */
    close (fd);
    free (fname);
    return sim_messagef (SCPE_OPENERR,
        "PANEL: %s belongs to uid %u, not to this user (uid %u)\n",
        cptr, (unsigned) st.st_uid, (unsigned) geteuid ());
    }
if ((st.st_mode & 077) != 0) {                          /* open to others? */
    close (fd);
    free (fname);
    return sim_messagef (SCPE_OPENERR,
        "PANEL: %s is open to other users (mode %04o; it must be 0600)\n",
        cptr, (unsigned) (st.st_mode & 07777));
    }
if (flock (fd, LOCK_EX | LOCK_NB) != 0) {               /* another simulator? */
    err = errno;
    close (fd);
    free (fname);
    return sim_messagef (SCPE_OPENERR, "PANEL: can't lock %s: %s\n", cptr,
        (err == EWOULDBLOCK)? "in use by another simulator": strerror (err));
    }
if ((st.st_size != (off_t) sizeof (PANEL_SHM)) &&
    (ftruncate (fd, (off_t) sizeof (PANEL_SHM)) != 0)) {
    err = errno;
    close (fd);
    free (fname);
    return sim_messagef (SCPE_OPENERR, "PANEL: can't size %s: %s\n", cptr, strerror (err));
    }
p = (PANEL_SHM *) mmap (NULL, sizeof (PANEL_SHM), PROT_READ | PROT_WRITE, MAP_SHARED, fd, 0);
if (p == (PANEL_SHM *) MAP_FAILED) {
    err = errno;
    close (fd);
    free (fname);
    return sim_messagef (SCPE_OPENERR, "PANEL: can't map %s: %s\n", cptr, strerror (err));
    }
panel_fd = fd;                                          /* keeps the lock */
if ((p->magic != PANEL_MAGIC) || (p->version != PANEL_VERSION) ||
    (p->size != sizeof (PANEL_SHM)) || (p->ring_size != PANEL_RING))
    memset ((void *) p, 0, sizeof (PANEL_SHM));         /* not ours: start clean */
panel_ring_wr = p->ring_wr;                             /* counters continue */
panel_seq = (p->seq + 1) & ~1u;                         /* even: not writing */
panel_stops = p->stop_count;
panel_traps = p->trap_count;
panel_icount = p->instr_count;
for (i = 0; i < 16; i++)
    panel_dr_on[i] = p->dr_on[i];
panel_wsum = p->weight_sum;
panel_halt_ack = __atomic_load_n (&p->halt_req, __ATOMIC_ACQUIRE);
PNL_PUBLISH (p->halt_ack, panel_halt_ack);              /* old requests are void */
PNL_PUBLISH (p->seq, panel_seq);
panel_state = PNL_ST_HALTED;
PNL_PUBLISH (p->state, panel_state);
p->version = PANEL_VERSION;
p->size = sizeof (PANEL_SHM);
p->ring_size = PANEL_RING;
PNL_PUBLISH (p->magic, PANEL_MAGIC);                    /* header complete */
now = panel_now ();
panel_interval = PNL_INT_INIT;
panel_samples = 0;
panel_sps = 0;
panel_cpc = 0.0;
panel_win_smp = 0;
panel_win_cnt = 0;
panel_win_wall = 0;
panel_win_work = 0;
panel_t_adapt = now;
panel_t_exit = now;
panel_running = FALSE;
panel_shm = p;
panel_regs ();
uptr->filename = fname;
uptr->flags = uptr->flags | UNIT_ATT;
return SCPE_OK;
}

/* Detach: unmap; the hooks see panel_shm == NULL */

t_stat panel_detach (UNIT *uptr)
{
PANEL_SHM *p = panel_shm;

if (!(uptr->flags & UNIT_ATT))
    return SCPE_UNATT;
panel_shm = NULL;
panel_wcountdown = PANEL_CD_NEVER;
panel_running = FALSE;
if (p != NULL) {
    PNL_PUBLISH (p->state, PNL_ST_HALTED);
    munmap ((void *) p, sizeof (PANEL_SHM));
    }
if (panel_fd >= 0) {
    close (panel_fd);                                   /* releases the lock */
    panel_fd = -1;
    }
free (uptr->filename);
uptr->filename = NULL;
uptr->flags = uptr->flags & ~UNIT_ATT;
return SCPE_OK;
}

t_stat panel_set_refresh (UNIT *uptr, int32 val, CONST char *cptr, void *desc)
{
if (cptr != NULL)
    return SCPE_ARG;
if (panel_shm == NULL)
    return SCPE_UNATT;
panel_regs ();
return SCPE_OK;
}

t_stat panel_show_status (FILE *st, UNIT *uptr, int32 val, CONST void *desc)
{
static const char *state_name[] = { "halted", "running", "wait" };

if (panel_shm == NULL) {
    fprintf (st, "export not attached");
    return SCPE_OK;
    }
fprintf (st, "export to %s, %u samples/sec (target %u), interval %d, %u samples, %s",
    uptr->filename, panel_sps, panel_target (), panel_interval, panel_samples,
    state_name[(panel_state <= PNL_ST_WAIT)? panel_state: PNL_ST_HALTED]);
return SCPE_OK;
}

#else                                                   /* no mmap: stubs */

int32 panel_sample (void)
{
return PANEL_CD_NEVER;
}

t_bool panel_wait (t_bool slept)
{
panel_wcountdown = PANEL_CD_NEVER;
return FALSE;
}

void panel_enter (void)
{
panel_wcountdown = PANEL_CD_NEVER;
}

int32 panel_rearm (void)
{
return PANEL_CD_NEVER;
}

void panel_exit (int32 cd)
{
}

void panel_trap (void)
{
}

int32 panel_sr (int32 sr)
{
return sr;
}

t_stat panel_attach (UNIT *uptr, CONST char *cptr)
{
return sim_messagef (SCPE_NOFNC, "PANEL: the front panel export needs mmap (POSIX hosts)\n");
}

t_stat panel_detach (UNIT *uptr)
{
return SCPE_UNATT;
}

t_stat panel_set_refresh (UNIT *uptr, int32 val, CONST char *cptr, void *desc)
{
return SCPE_UNATT;
}

t_stat panel_show_status (FILE *st, UNIT *uptr, int32 val, CONST void *desc)
{
fprintf (st, "export not supported on this host");
return SCPE_OK;
}

#endif                                                  /* PANEL_HAVE_SHM */

const char *panel_description (DEVICE *dptr)
{
return "front panel export (shared memory)";
}

t_stat panel_help (FILE *st, DEVICE *dptr, UNIT *uptr, int32 flag, const char *cptr)
{
fprintf (st, "Front panel export (PANEL)\n\n");
fprintf (st, "The PANEL device writes bus-cycle samples weighted with wall-clock time, the\n");
fprintf (st, "registers (R0-R7, PSW, DR, MMR0, MMR3), the run/WAIT state and the ADRS ERR\n");
fprintf (st, "trap count into a shared memory file, and reads the switch register and halt\n");
fprintf (st, "requests from it, so that an external program can drive a front panel:\n\n");
fprintf (st, "   sim> ATTACH PANEL /dev/shm/pdp11-panel\n\n");
fprintf (st, "The file layout is defined in pdp11_panel_shm.h.  While the CPU runs, about\n");
fprintf (st, "target_rate samples per second are taken (50000 by default).  A halt request\n");
fprintf (st, "stops the CPU with \"Front panel HALT\".  SET PANEL REFRESH rewrites the\n");
fprintf (st, "register block, e.g. after DEPOSIT while the CPU is halted.  Without an\n");
fprintf (st, "attached file the device does nothing.\n");
fprint_set_help (st, dptr);
fprint_show_help (st, dptr);
fprint_reg_help (st, dptr);
return SCPE_OK;
}
