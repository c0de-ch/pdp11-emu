/* pdp11_panel.h: PDP-11 front panel export, simulator side

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

   The PANEL device (pdp11_panel.c) maps a file with "ATTACH PANEL <file>"
   and exports bus-cycle samples, the register block, the run state, a live
   switch register and a halt handshake to an external program.  The file
   layout and the protocol are defined in pdp11_panel_shm.h.

   Hooks in the CPU (pdp11_cpu.c) and the system registers (pdp11_cpumod.c):

   panel_enter ()           sim_instr entry
   panel_cd                 sim_instr local countdown (it can live in a
                            register), set to panel_rearm () before the main
                            loop (at entry and after every abort) and
                            decremented once per instruction, before inst_pc
                            is updated.  When it expires, panel_cd =
                            panel_sample (); if that set reason (STOP_PANEL:
                            halt requested) the CPU stops before the next
                            instruction is fetched.
   panel_bus = 0            then, every instruction: no bus cycle captured yet
   PANEL_VA (va)            virtual data references (ReadW, ReadB, ReadCW,
                            ReadMW, ReadMB, WriteW, WriteB, WriteCW, and the
                            last word of the FP11's multi-word stores, which
                            go through PWriteW): virtual address including the
                            D space bit <16> and the mode <18:17>
   PANEL_BUS (pa,d,fl)      physical funnel (PReadW, PReadB, PWriteW, PWriteB),
                            memory and I/O page paths: one 64-bit store
   --panel_wcountdown       every WAIT state iteration, before sim_idle;
                            panel_wait (FALSE) when it expires (it returns
                            TRUE when a halt was requested: no idling then),
                            panel_wait (TRUE) when sim_idle slept
   panel_exit (panel_cd)    sim_instr exit, after the registers were saved
   panel_trap ()            red stack, odd address, NXM and MMU trap dispatch
   SR = panel_sr (SR)       switch register read (SR_rd)

   panel_bus: <31:0> PA<21:0> | PNL_WR/PNL_BYTE/PNL_IOPAGE | PANEL_CYCLE,
   <47:32> data.  When it is 0 at a sample, the last instruction made no data
   reference after its fetch: the sample shows that fetch, rebuilt from
   inst_pc (relocated in the mode of inst_psw) and memory.  The IR of a sample
   is rebuilt the same way, so the fetch path itself carries no hook.

   When no file is attached panel_shm is NULL, the countdowns stay near
   PANEL_CD_NEVER and every hook is a no-op apart from the plain stores.
*/

#ifndef PDP11_PANEL_H_
#define PDP11_PANEL_H_  0

#include "pdp11_panel_shm.h"

#define PANEL_CD_NEVER  0x7FFFFFFF                      /* countdown when detached */
#define PANEL_CYCLE     (1u << PNL_V_MODE)              /* panel_bus holds a cycle */

extern PANEL_SHM *panel_shm;                            /* NULL: not attached */
extern t_uint64 panel_bus;                              /* last data cycle */
extern uint32 panel_bus_va;                             /* last data virtual address */
extern int32 panel_wcountdown;                          /* WAIT iterations to sample */

int32 panel_sample (void);
t_bool panel_wait (t_bool slept);
void panel_enter (void);
int32 panel_rearm (void);
void panel_exit (int32 cd);
void panel_trap (void);
int32 panel_sr (int32 sr);

#define PANEL_BUS(pa,d,fl) \
    (panel_bus = ((t_uint64) (uint16) (d) << 32) | (uint32) (pa) | (fl) | PANEL_CYCLE)
#define PANEL_VA(va)        (panel_bus_va = (uint32) (va))

#endif
