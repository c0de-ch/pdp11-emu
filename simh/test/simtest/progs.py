"""PDP-11 test programs (simtest.asm11 syntax)."""

from .asm11 import assemble

# Kernel-mode program at 1000: rotating bit into the display register,
# R5 <- switch register, SR<15> = 0: busy delay loop, SR<15> = 1: WAIT until
# the next clock tick; every 32 loops one letter A..Z on the console; clock
# interrupt (KW11-L, vector 100) counts ticks at 3000.  R0 = 012345 throughout.
BLINKER = """
TICKS = 3000
        .org 100
        .word clk, 340
        .org 1000
start:  mov #1000, sp
        mov #12345, r0          ; R0 shows in WAIT samples
        mov #1, r1
        mov #40, r2
        mov #'A, r4
        mov #100, @#177546      ; KW11-L interrupt enable
        clr @#177776            ; PSW: kernel, priority 0
loop:   mov r1, @#177570        ; DR <- rotating bit
        clc
        rol r1
        adc r1
        mov @#177570, r5        ; R5 <- SR
        bpl busy                ; SR<15> = 0
idle:   wait                    ; SR<15> = 1
waitpc: br count
busy:   mov #1000, r3
dly:    sob r3, dly
count:  dec r2
        bne loop
        mov #40, r2
out:    tstb @#177564           ; console ready?
        bpl out
        movb r4, @#177566
        inc r4
        cmp r4, #'Z+1
        bne loop
        mov #'A, r4
        br loop
clk:    inc @#TICKS
        rti
"""

# Kernel part of the memory management test (at 1000, MMU off at start):
# kernel I page 0 -> PA 0, page 7 -> I/O page; user I page 0 -> PA 400000,
# user D page 0 -> PA 600000; 22-bit mapping, user D space; then RTI to the
# user program at VA 1000 with the clock interrupt enabled.
MMU_KERNEL = """
KISAR0 = 172340
KISDR0 = 172300
KISAR7 = 172356
KISDR7 = 172316
UISAR0 = 177640
UISDR0 = 177600
UDSAR0 = 177660
UDSDR0 = 177620
MMR0 = 177572
MMR3 = 172516
        .org 100
        .word clk, 340
        .org 1000
kstart: mov #1000, sp
        clr @#KISAR0
        mov #77406, @#KISDR0
        mov #177600, @#KISAR7
        mov #77406, @#KISDR7
        mov #4000, @#UISAR0
        mov #77406, @#UISDR0
        mov #6000, @#UDSAR0
        mov #77406, @#UDSDR0
        mov #21, @#MMR3         ; 22-bit mapping, user D space
        mov #1, @#MMR0          ; relocation on
        mov #100, @#177546      ; clock interrupts
        mov #140000, -(sp)      ; user mode
        mov #1000, -(sp)
        rti
clk:    inc @#3000
        rti
"""
MMU_USER_PA = 0o400000                  # user I page 0
MMU_DATA_PA = 0o600000                  # user D page 0

# User program, VA 1000 (load at PA 401000): D-space counter at VA 2000;
# the word at VA 2002 selects busy (0) or WAIT (else).
MMU_USER = """
        .org 1000
ustart: mov #12345, r0
uloop:  inc @#2000              ; D space read-modify-write
        tst @#2002              ; D space read: mode flag
        beq uloop
uwait:  wait
uwpc:   br uloop
"""

# Odd address trap (vector 4) at 2000; the handler halts.
ODD_TRAP = """
        .org 4
        .word trap4, 340
        .org 2000
tstart: mov #1000, sp
        mov @#1001, r0          ; odd address -> trap 4
        halt
trap4:  halt
"""

# Non-existent memory trap (vector 4) at 2000, for a 32 KB machine.
NXM_TRAP = """
        .org 4
        .word ntrap, 340
        .org 2000
nstart: mov #1000, sp
        tst @#100000            ; above 32 KB -> trap 4
        halt
ntrap:  halt
"""

# T bit tracing at 7000: every traced instruction (INC R0, BR) traps to 14,
# whose handler counts in R1 and returns with RTT; so R1 = 2 * R0 (+-1).
TRACER = """
        .org 14
        .word trc, 340
        .org 7000
tstart: mov #1000, sp
        clr r0
        clr r1
        mov #20, -(sp)          ; PSW with the T bit
        mov #tloop, -(sp)
        rtt
tloop:  inc r0
        br tloop
trc:    inc r1
        rtt
"""

# Halts after a delay of about 1-5 ms, repeatedly (CONT resumes after HALT).
HALTER = """
        .org 6000
hstart: mov #1000, sp
again:  mov #200, r4
outer:  mov #10000, r3
inner:  sob r3, inner
        sob r4, outer
hhalt:  halt
        br again
"""

# An odd address abort in every loop (vector 4: RTI), at 2000: 4 instructions
# per loop (INC, the aborted TST, RTI, BR), no WAIT.
ABORT_LOOP = """
        .org 4
        .word oddh, 340
        .org 2000
astart: mov #1000, sp
        clr r5
aloop:  inc r5
        tst @#1001              ; odd address -> trap 4
        br aloop
oddh:   rti
"""

# Kernel at 1000 with the MMU on (kernel I page 0 -> PA 0, page 7 -> I/O
# page, user I page 0 -> PA 400000, 22-bit), previous mode user.  MTPI_LOOP:
# the MTPI writes user VA 100 (PA 400100) in the previous mode.  RTI_LOOP:
# RTI pops the kernel stack (PC at 774, PSW at 776) into user mode, where the
# TRAP at user VA 0 (load 104400 at PA 400000) returns to the kernel.
MODE_SETUP = """
        .org 34
        .word trh, 340
        .org 1000
mstart: mov #1000, sp
        mov #77406, @#172300    ; KISDR0
        clr @#172340            ; KISAR0 -> PA 0
        mov #77406, @#172316    ; KISDR7
        mov #177600, @#172356   ; KISAR7 -> I/O page
        mov #77406, @#177600    ; UISDR0
        mov #4000, @#177640     ; UISAR0 -> PA 400000
        mov #20, @#172516       ; MMR3: 22-bit
        mov #1, @#177572        ; MMR0: relocation on
        mov #30000, @#177776    ; PSW: previous mode user
"""
MTPI_LOOP = MODE_SETUP + """
mloop:  mov #1234, -(sp)
        mtpi @#100
        br mloop
trh:    halt
"""
RTI_LOOP = MODE_SETUP + """
rloop:  mov #170000, -(sp)      ; user mode, previous user
        clr -(sp)               ; user PC 0
        rti
trh:    add #4, sp
        br rloop
"""
MODE_USER_TRAP = 0o104400               # TRAP 0, at PA 400000

# FP11 stores at 1000 (MMU off): STD writes 4 words at 3000 (the last at
# 3006, through WriteFP), STCFI in long integer mode 2 words at 4000 (the last
# at 4002, through WriteI).  The FP instructions are .words (the assembler
# has no FP11 mnemonics).
FP_STORE = """
        .org 1000
fstart: mov #1000, sp
floop:  .word 170011            ; SETD
        .word 174037, 3000      ; STD AC0, @#3000
        .word 170012            ; SETL
        .word 175437, 4000      ; STCFI AC0, @#4000
        br floop
"""

# DISPLAY REGISTER duty at 1000: DR = 1 for 66 of every 261 instruction
# boundaries, DR = 2 for the other 195 (bit 0 on 25.3 %, bit 1 74.7 %).
DR_DUTY = """
        .org 1000
dstart: mov #1000, sp
dloop:  mov #1, @#177570
        mov #100, r3
d1:     sob r3, d1
        mov #2, @#177570
        mov #300, r3
d2:     sob r3, d2
        br dloop
"""
DR_DUTY_BIT0 = 66 / 261.0

# MASTER (I/O page cycles) on for a fifth of the instruction boundaries, at 1000.
IO_FIFTH = """
        .org 1000
istart: mov #1000, sp
        clr r1
iloop:  mov r1, @#177570
        inc r2
        inc r3
        inc r4
        br iloop
"""

# Benchmarks (research r1, exp6): a SOB loop and a memory-copy + JSR/RTS mix.
SOB_LOOP = """
        .org 4000
sob0:   mov #177777, r3
sob1:   sob r3, sob1
        br sob0
"""
COPY_MIX = """
        .org 5000
mix:    mov #1000, sp
mloop:  mov #14000, r1
        mov #15000, r2
        mov #20, r3
cpy:    mov (r1)+, (r2)+
        add r3, r4
        sob r3, cpy
        jsr pc, sub
ret:    inc @#3002
        br mloop
sub:    mov r4, r5
        asl r5
        xor r4, r5
        rts pc
"""

ALL = {"BLINKER": BLINKER, "MMU_KERNEL": MMU_KERNEL, "MMU_USER": MMU_USER,
       "ODD_TRAP": ODD_TRAP, "NXM_TRAP": NXM_TRAP, "TRACER": TRACER, "HALTER": HALTER,
       "ABORT_LOOP": ABORT_LOOP, "MTPI_LOOP": MTPI_LOOP, "RTI_LOOP": RTI_LOOP,
       "FP_STORE": FP_STORE, "DR_DUTY": DR_DUTY, "IO_FIFTH": IO_FIFTH,
       "SOB_LOOP": SOB_LOOP, "COPY_MIX": COPY_MIX}


def program(name):
    return assemble(ALL[name])


def deposits(prog, offset=0):
    """DEPOSIT commands; offset relocates the load address (physical)."""
    return ["deposit %o %o" % (a + offset, w) for a, w in prog.words]
