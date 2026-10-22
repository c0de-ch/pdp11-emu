"""A small two-pass PDP-11 assembler for test programs.

Syntax (a subset of MACRO-11)::

    label:  opcode  operand, operand    ; comment
            .org    expr                ; set the location counter (also ". = expr")
            .word   expr, ...
            .blkw   expr                ; expr zero words
    name    =       expr                ; symbol assignment

Numbers are octal; a trailing '.' makes a number decimal.  An expression is a
sum or difference of terms; a term is a number, a symbol, '.' (the address of
the current statement) or 'c (a character).  Operands: Rn (R0-R5, SP, PC),
(Rn), (Rn)+, @(Rn)+, -(Rn), @-(Rn), e(Rn), @e(Rn), #e, @#e, e (PC relative)
and @e (PC relative deferred).

assemble() returns a Program: the words, the symbol table and, for every
instruction, the text SimH's "EXAMINE -M" prints for it, so that a test can
check the assembler against SimH's own disassembler.
"""

import re

REGS = {"R0": 0, "R1": 1, "R2": 2, "R3": 3, "R4": 4, "R5": 5, "SP": 6, "PC": 7,
        "R6": 6, "R7": 7}
REG_NAMES = ["R0", "R1", "R2", "R3", "R4", "R5", "SP", "PC"]

# mnemonic -> (class, opcode)
OPS = {}
for _n, _v in {"HALT": 0, "WAIT": 1, "RTI": 2, "BPT": 3, "IOT": 4, "RESET": 5,
               "RTT": 6, "NOP": 0o240, "CLC": 0o241, "CLV": 0o242, "CLZ": 0o244,
               "CLN": 0o250, "CCC": 0o257, "SEC": 0o261, "SEV": 0o262,
               "SEZ": 0o264, "SEN": 0o270, "SCC": 0o277}.items():
    OPS[_n] = ("none", _v)
for _n, _v in {"JMP": 0o0001, "SWAB": 0o0003, "CLR": 0o0050, "COM": 0o0051,
               "INC": 0o0052, "DEC": 0o0053, "NEG": 0o0054, "ADC": 0o0055,
               "SBC": 0o0056, "TST": 0o0057, "ROR": 0o0060, "ROL": 0o0061,
               "ASR": 0o0062, "ASL": 0o0063, "MFPI": 0o0065, "MTPI": 0o0066,
               "SXT": 0o0067, "CLRB": 0o1050, "COMB": 0o1051, "INCB": 0o1052,
               "DECB": 0o1053, "NEGB": 0o1054, "ADCB": 0o1055, "SBCB": 0o1056,
               "TSTB": 0o1057, "RORB": 0o1060, "ROLB": 0o1061, "ASRB": 0o1062,
               "ASLB": 0o1063, "MFPD": 0o1065, "MTPD": 0o1066}.items():
    OPS[_n] = ("one", _v << 6)
for _n, _v in {"MOV": 0o01, "CMP": 0o02, "BIT": 0o03, "BIC": 0o04, "BIS": 0o05,
               "ADD": 0o06, "MOVB": 0o11, "CMPB": 0o12, "BITB": 0o13,
               "BICB": 0o14, "BISB": 0o15, "SUB": 0o16}.items():
    OPS[_n] = ("two", _v << 12)
for _n, _v in {"BR": 0o000400, "BNE": 0o001000, "BEQ": 0o001400, "BGE": 0o002000,
               "BLT": 0o002400, "BGT": 0o003000, "BLE": 0o003400, "BPL": 0o100000,
               "BMI": 0o100400, "BHI": 0o101000, "BLOS": 0o101400, "BVC": 0o102000,
               "BVS": 0o102400, "BCC": 0o103000, "BHIS": 0o103000, "BCS": 0o103400,
               "BLO": 0o103400}.items():
    OPS[_n] = ("branch", _v)
OPS.update({"JSR": ("reg_dst", 0o004000), "XOR": ("reg_dst", 0o074000),
            "MUL": ("src_reg", 0o070000), "DIV": ("src_reg", 0o071000),
            "ASH": ("src_reg", 0o072000), "ASHC": ("src_reg", 0o073000),
            "SOB": ("sob", 0o077000), "RTS": ("rts", 0o000200),
            "SPL": ("spl", 0o000230), "EMT": ("trap8", 0o104000),
            "TRAP": ("trap8", 0o104400)})
# SimH prints these under another name
SIMH_ALIAS = {"BHIS": "BCC", "BLO": "BCS"}


class AsmError(Exception):
    pass


def _split_operands(text):
    """Split 'a, b' at the top-level comma (no commas inside parentheses)."""
    out, depth, cur = [], 0, ""
    for ch in text:
        if ch == "(":
            depth += 1
        elif ch == ")":
            depth -= 1
        if ch == "," and depth == 0:
            out.append(cur.strip())
            cur = ""
        else:
            cur += ch
    if cur.strip():
        out.append(cur.strip())
    return out


class _Operand:
    """One parsed operand: mode, register and the expression of its extra
    word; rel marks the PC-relative forms written as e and @e."""

    def __init__(self, mode, reg, expr=None, rel=False):
        self.mode, self.reg, self.expr, self.rel = mode, reg, expr, rel

    @property
    def spec(self):
        return (self.mode << 3) | self.reg

    @property
    def has_word(self):
        return self.mode >= 6 or (self.reg == 7 and self.mode in (2, 3))


class Program:
    def __init__(self, words, symbols, listing):
        self.words = words          # [(address, word)] in source order
        self.symbols = symbols      # name -> value
        self.listing = listing      # [(address, nwords, simh_text, source)]

    def __getitem__(self, name):
        return self.symbols[name.upper()]

    def deposits(self):
        """SCP DEPOSIT commands that load the program."""
        return ["deposit %o %o" % (a, w) for a, w in self.words]

    def word_at(self, addr):
        for a, w in self.words:
            if a == addr:
                return w
        raise KeyError("%o" % addr)


class Assembler:
    _num = re.compile(r"^(\d+)(\.?)$")

    def __init__(self, source):
        self.lines = source.splitlines()
        self.symbols = {}

    # ---- expressions -------------------------------------------------------

    def _value(self, text, dot, final):
        s = "".join(text.split())
        if not s:
            raise AsmError("empty expression")
        total, sign, i = 0, 1, 0
        while i < len(s):
            if s[i] in "+-":                        # binary or unary sign
                sign = -sign if s[i] == "-" else sign
                i += 1
                continue
            if s[i] == "'":                         # character
                if i + 1 >= len(s):
                    raise AsmError("bad character constant")
                total += sign * ord(s[i + 1])
                i += 2
            else:
                j = i
                while j < len(s) and s[j] not in "+-":
                    j += 1
                total += sign * self._term(s[i:j], dot, final)
                i = j
            sign = 1
        return total & 0o177777

    def _term(self, t, dot, final):
        u = t.upper()
        m = self._num.match(u)
        if m:
            return int(m.group(1), 10 if m.group(2) else 8)
        if u == ".":
            return dot
        if u in self.symbols:
            return self.symbols[u]
        if final:
            raise AsmError("undefined symbol %r" % t)
        return 0

    # ---- operands ----------------------------------------------------------

    def _operand(self, text):
        t = text.strip()
        u = t.upper()
        if u in REGS:
            return _Operand(0, REGS[u])
        m = re.match(r"^(@?)\((\w+)\)(\+?)$", u)
        if m and m.group(2) in REGS:
            r = REGS[m.group(2)]
            if m.group(3):
                return _Operand(3 if m.group(1) else 2, r)
            if m.group(1):
                return _Operand(7, r, "0")          # @(Rn) = @0(Rn)
            return _Operand(1, r)
        m = re.match(r"^(@?)-\((\w+)\)$", u)
        if m and m.group(2) in REGS:
            return _Operand(5 if m.group(1) else 4, REGS[m.group(2)])
        if u.startswith("@#"):
            return _Operand(3, 7, t[2:])
        if u.startswith("#"):
            return _Operand(2, 7, t[1:])
        m = re.match(r"^(@?)(.+)\((\w+)\)$", t)
        if m and m.group(3).upper() in REGS:
            return _Operand(7 if m.group(1) else 6, REGS[m.group(3).upper()], m.group(2))
        if t.startswith("@"):
            return _Operand(7, 7, t[1:], rel=True)
        return _Operand(6, 7, t, rel=True)

    # ---- statements ----------------------------------------------------------

    def _statements(self):
        for lineno, raw in enumerate(self.lines, 1):
            line = raw.split(";", 1)[0].rstrip()
            if not line.strip():
                continue
            labels = []
            while True:
                m = re.match(r"^\s*([A-Za-z_$][\w$]*):(.*)$", line)
                if not m:
                    break
                labels.append(m.group(1).upper())
                line = m.group(2)
            m = re.match(r"^\s*([A-Za-z_$.][\w$.]*)\s*=\s*(.+)$", line)
            if m:
                yield lineno, raw, labels, "=", [m.group(1).upper(), m.group(2)]
                continue
            parts = line.strip().split(None, 1)
            op = parts[0].upper() if parts else ""
            args = _split_operands(parts[1]) if len(parts) > 1 else []
            yield lineno, raw, labels, op, args

    def assemble(self):
        stmts = list(self._statements())
        for final in (False, True):
            dot, words, listing = 0, [], []
            for lineno, raw, labels, op, args in stmts:
                for lab in labels:
                    if not final and lab in self.symbols and self.symbols[lab] != dot:
                        raise AsmError("line %d: %s defined twice" % (lineno, lab))
                    self.symbols[lab] = dot
                try:
                    dot = self._statement(op, args, dot, final, words, listing, raw)
                except AsmError as e:
                    raise AsmError("line %d: %s (%s)" % (lineno, e, raw.strip()))
        return Program(words, dict(self.symbols), listing)

    def _statement(self, op, args, dot, final, words, listing, raw):
        val = lambda e: self._value(e, dot, final)
        if op == "":
            return dot
        if op == "=":
            name, expr = args
            if name == ".":
                return val(expr)
            self.symbols[name] = val(expr)
            return dot
        if op == ".ORG":
            return val(args[0])
        if op == ".WORD":
            for a in args:
                words.append((dot, val(a)))
                dot += 2
            return dot
        if op == ".BLKW":
            for _ in range(val(args[0])):
                words.append((dot, 0))
                dot += 2
            return dot
        if op not in OPS:
            raise AsmError("unknown opcode %s" % op)
        if dot & 1:
            raise AsmError("odd address")
        cls, opc = OPS[op]
        name = SIMH_ALIAS.get(op, op)
        start = dot
        out = []                                    # words of this instruction

        def emit(w):
            out.append(w & 0o177777)

        def need(n):
            if len(args) != n:
                raise AsmError("%s needs %d operand(s)" % (op, n))

        def reg(a):
            u = a.strip().upper()
            if u not in REGS:
                raise AsmError("%s is not a register" % a)
            return REGS[u]

        def operand_word(o, word_addr):
            """Extra word of operand o stored at word_addr, and the value
            SimH shows for it (the target address for PC modes 6 and 7)."""
            v = val(o.expr)
            if o.reg == 7 and o.mode in (6, 7):
                if o.rel:
                    return (v - (word_addr + 2)) & 0o177777, v
                return v, (v + word_addr + 2) & 0o177777
            return v, v

        def text(o, value):
            r = REG_NAMES[o.reg]
            if o.mode == 0:
                return r
            if o.mode == 1:
                return "(%s)" % r
            if o.mode == 2:
                return "#%o" % value if o.reg == 7 else "(%s)+" % r
            if o.mode == 3:
                return "@#%o" % value if o.reg == 7 else "@(%s)+" % r
            if o.mode == 4:
                return "-(%s)" % r
            if o.mode == 5:
                return "@-(%s)" % r
            if o.reg == 7:
                return ("@%o" if o.mode == 7 else "%o") % value
            return ("@%o(%s)" if o.mode == 7 else "%o(%s)") % (value, r)

        if cls == "none":
            need(0)
            emit(opc)
            simh = name
        elif cls == "one":
            need(1)
            o = self._operand(args[0])
            emit(opc | o.spec)
            tv = None
            if o.has_word:
                w, tv = operand_word(o, start + 2)
                emit(w)
            simh = "%s %s" % (name, text(o, tv))
        elif cls == "two":
            need(2)
            s, d = self._operand(args[0]), self._operand(args[1])
            emit(opc | (s.spec << 6) | d.spec)
            sv = dv = None
            if s.has_word:
                w, sv = operand_word(s, start + 2 * len(out))
                emit(w)
            if d.has_word:
                w, dv = operand_word(d, start + 2 * len(out))
                emit(w)
            simh = "%s %s,%s" % (name, text(s, sv), text(d, dv))
        elif cls == "reg_dst":
            need(2)
            r, d = reg(args[0]), self._operand(args[1])
            emit(opc | (r << 6) | d.spec)
            dv = None
            if d.has_word:
                w, dv = operand_word(d, start + 2)
                emit(w)
            simh = "%s %s,%s" % (name, REG_NAMES[r], text(d, dv))
        elif cls == "src_reg":
            need(2)
            s, r = self._operand(args[0]), reg(args[1])
            emit(opc | (r << 6) | s.spec)
            sv = None
            if s.has_word:
                w, sv = operand_word(s, start + 2)
                emit(w)
            simh = "%s %s,%s" % (name, text(s, sv), REG_NAMES[r])
        elif cls == "branch":
            need(1)
            target = val(args[0])
            off = (target - (start + 2)) // 2
            if final and not -128 <= off <= 127:
                raise AsmError("branch out of range")
            emit(opc | (off & 0o377))
            simh = "%s %o" % (name, target)
        elif cls == "sob":
            need(2)
            r, target = reg(args[0]), val(args[1])
            off = ((start + 2) - target) // 2
            if final and not 0 <= off <= 0o77:
                raise AsmError("SOB out of range")
            emit(opc | (r << 6) | (off & 0o77))
            simh = "%s %s,%o" % (name, REG_NAMES[r], target)
        elif cls == "rts":
            need(1)
            r = reg(args[0])
            emit(opc | r)
            simh = "%s %s" % (name, REG_NAMES[r])
        elif cls == "spl":
            need(1)
            n = val(args[0]) & 7
            emit(opc | n)
            simh = "%s %o" % (name, n)
        else:                                       # trap8
            n = val(args[0]) & 0o377 if args else 0
            emit(opc | n)
            simh = "%s %o" % (name, n)
        for i, w in enumerate(out):
            words.append((start + 2 * i, w))
        listing.append((start, len(out), simh, raw.strip()))
        return start + 2 * len(out)


def assemble(source):
    """Assemble source text; raises AsmError with the line number on errors."""
    return Assembler(source).assemble()
