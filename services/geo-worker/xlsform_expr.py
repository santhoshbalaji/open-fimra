"""Translate between form-format conditions and calculations and XLSForm expressions.

XLSForm writes conditions and calculations in XPath, with ${name} for another
question and "." for the question itself. This module reads the part of XPath
that has a meaning in the form format (SPEC.md sections 10 and 11) and writes
form-format conditions and calculations back in the same part. Anything else
raises Untranslatable with a plain-English reason, so the caller can list it in
the review report instead of guessing.
"""

import re
from dataclasses import dataclass
from decimal import Decimal


class Untranslatable(Exception):
    """An expression has no exact form-format meaning. The message says why."""


# ---------------------------------------------------------------------------
# Reading XPath

_TOKEN = re.compile(r"""
    (?P<space>\s+)
  | (?P<ref>\$\{\s*(?P<refname>[^}]*?)\s*\})
  | (?P<str>'[^']*'|"[^"]*")
  | (?P<num>[0-9]+(?:\.[0-9]*)?|\.[0-9]+)
  | (?P<op><=|>=|!=|=|<|>|\+|-|\*|\(|\)|,)
  | (?P<name>[A-Za-z_][A-Za-z0-9_.-]*)
  | (?P<dot>\.)
""", re.VERBOSE)

_WORD_OPS = {"and", "or", "div", "mod"}
_LEVELS = [("or",), ("and",), ("=", "!="), ("<", "<=", ">", ">="), ("+", "-"), ("*", "div", "mod")]


@dataclass(frozen=True)
class Node:
    """One part of a parsed expression.

    kind is ref, dot, str, num, name, call, bin, neg or paren. A paren node
    records brackets written in the text, so nested groups keep their shape.
    """
    kind: str
    value: object = None
    args: tuple = ()


def _tokens(text):
    pos, out = 0, []
    while pos < len(text):
        m = _TOKEN.match(text, pos)
        if not m:
            raise Untranslatable(f"it uses “{text[pos]}”, which Open FIMRA cannot read here")
        pos = m.end()
        kind = m.lastgroup
        if kind == "space":
            continue
        if kind == "ref":
            out.append(("ref", m.group("refname")))
        elif kind == "str":
            out.append(("str", m.group()[1:-1]))
        elif kind == "num":
            out.append(("num", m.group()))
        elif kind == "name" and m.group() in _WORD_OPS:
            out.append(("op", m.group()))
        else:
            out.append((kind, m.group()))
    return out


class _Parser:
    def __init__(self, text):
        self.toks = _tokens(text)
        self.i = 0

    def peek(self):
        return self.toks[self.i] if self.i < len(self.toks) else (None, None)

    def take(self, kind=None, value=None):
        tok = self.peek()
        if tok[0] is None or (kind and tok[0] != kind) or (value and tok[1] != value):
            want = value or kind or "more"
            raise Untranslatable(f"it is not a complete expression (expected “{want}”)")
        self.i += 1
        return tok

    def parse(self):
        if not self.toks:
            raise Untranslatable("it is empty")
        node = self.level(0)
        if self.i != len(self.toks):
            raise Untranslatable(f"it has more after the end of the expression, starting at “{self.peek()[1]}”")
        return node

    def level(self, n):
        if n == len(_LEVELS):
            return self.unary()
        node = self.level(n + 1)
        while self.peek()[0] == "op" and self.peek()[1] in _LEVELS[n]:
            op = self.take()[1]
            node = Node("bin", op, (node, self.level(n + 1)))
        return node

    def unary(self):
        if self.peek() == ("op", "-"):
            self.take()
            return Node("neg", None, (self.unary(),))
        return self.primary()

    def primary(self):
        kind, value = self.take()
        if kind == "ref":
            return Node("ref", value)
        if kind == "dot":
            return Node("dot")
        if kind == "str":
            return Node("str", value)
        if kind == "num":
            return Node("num", float(value) if "." in value else int(value))
        if kind == "op" and value == "(":
            inner = self.level(0)
            self.take("op", ")")
            return Node("paren", None, (inner,))
        if kind == "name":
            if self.peek() == ("op", "("):
                self.take()
                args = []
                if self.peek() != ("op", ")"):
                    args.append(self.level(0))
                    while self.peek() == ("op", ","):
                        self.take()
                        args.append(self.level(0))
                self.take("op", ")")
                return Node("call", value, tuple(args))
            return Node("name", value)
        raise Untranslatable(f"it has “{value}” where a value was expected")


def parse(text):
    """Parse an XLSForm expression, or raise Untranslatable."""
    return _Parser(text).parse()


def bare(node):
    while node.kind == "paren":
        node = node.args[0]
    return node


def _chain(node, op):
    """The operands of a chain such as a and b and c, not looking inside brackets."""
    if node.kind == "bin" and node.value == op:
        return _chain(node.args[0], op) + _chain(node.args[1], op)
    return [node]


# ---------------------------------------------------------------------------
# What a question looks like to an expression


@dataclass
class Ref:
    """A question an expression can name.

    value_type follows SPEC.md section 13, or is "group" or "repeat".
    repeats lists the keys of the repeating groups the question is inside,
    outermost first. A repeating group's own list ends with its key.
    """
    key: str
    item_type: str
    value_type: str
    repeats: tuple = ()
    choices: dict | None = None  # XLSForm choice name -> choice key
    inside: tuple = ()  # keys of every group and repeating group the question is in


NUMBER_TYPES = {"number"}
TIME_TYPES = {"date", "time", "date-time"}
_COMPARE = {"=": "equals", "<": "less-than", "<=": "at-most", ">": "more-than", ">=": "at-least"}
_FLIP = {"=": "=", "!=": "!=", "<": ">", "<=": ">=", ">": "<", ">=": "<="}
_DATE = re.compile(r"^[0-9]{4}-[0-9]{2}-[0-9]{2}$")
_TIME = re.compile(r"^([0-9]{2}:[0-9]{2})(:00(\.0+)?)?$")
_DATE_TIME = re.compile(r"^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}(:[0-9]{2})?(Z|[+-][0-9]{2}:[0-9]{2})$")


def _describe(node):
    node = bare(node)
    if node.kind == "call":
        return f"the function {node.value}()"
    if node.kind == "name":
        return f"“{node.value}”"
    if node.kind == "dot":
        return "the question itself (.)"
    return "this kind of expression"


AMBIGUOUS = object()  # stands for a name that more than one question has


class Resolver:
    """Looks up ${name} for one place in the form."""

    def __init__(self, refs, here_repeats=(), owner=None):
        self.refs = refs  # XLSForm name -> Ref, or None when the question was not imported
        self.here = tuple(here_repeats)
        self.owner = owner  # the key of the question or group the expression belongs to

    def ref(self, node):
        node = bare(node)
        if node.kind != "ref":
            raise Untranslatable(f"it uses {_describe(node)} where a question was expected")
        name = node.value
        if name not in self.refs:
            raise Untranslatable(f"it refers to “{name}”, which is not a question in the form")
        ref = self.refs[name]
        if ref is None:
            raise Untranslatable(f"it refers to “{name}”, which was not imported")
        if ref is AMBIGUOUS:
            raise Untranslatable(f"it refers to “{name}”, which is the name of more than one question")
        if self.owner and ref.key == self.owner:
            raise Untranslatable("it refers to the question itself")
        if self.owner and self.owner in ref.inside:
            raise Untranslatable(f"it refers to “{ref.key}”, which is inside this group, so the group would depend "
                                 "on its own questions")
        return ref

    def answer(self, node):
        """A question whose single answer a condition can test."""
        ref = self.ref(node)
        if ref.value_type in ("group", "repeat"):
            raise Untranslatable(f"it refers to the group “{ref.key}”, not to a question")
        if ref.repeats and ref.repeats != self.here[:len(ref.repeats)]:
            raise Untranslatable(f"it refers to “{ref.key}”, which is inside a repeating group that this part "
                                 "of the form is not in, so it has many answers")
        return ref


# ---------------------------------------------------------------------------
# Conditions


def _literal(node, ref, op):
    """The form-format value that a comparison with ref uses."""
    node = bare(node)
    vt = ref.value_type
    if node.kind == "call" and node.value not in ("date", "date-time", "decimal-time"):
        raise Untranslatable(f"it compares “{ref.key}” with {node.value}(), and the form format compares answers "
                             "only with fixed values")
    if node.kind == "neg" and bare(node.args[0]).kind == "num":
        node = Node("num", -bare(node.args[0]).value)
    if vt == "number":
        if node.kind == "num":
            return node.value
        if node.kind == "str":
            try:
                number = Decimal(node.value.strip())
            except ArithmeticError:
                number = None
            if number is not None and number.is_finite():
                return int(number) if number == number.to_integral_value() else float(number)
        raise Untranslatable(f"it compares the number “{ref.key}” with something that is not a number")
    if vt == "date":
        if node.kind == "call" and node.value == "date" and len(node.args) == 1:
            node = bare(node.args[0])
        if node.kind == "str" and _DATE.match(node.value):
            return node.value
        raise Untranslatable(f"it compares the date “{ref.key}” with something that is not a fixed date such as "
                             "date('2026-03-15')")
    if vt == "date-time":
        if node.kind == "call" and node.value == "date-time" and len(node.args) == 1:
            node = bare(node.args[0])
        if node.kind == "str" and _DATE_TIME.match(node.value):
            return node.value
        raise Untranslatable(f"it compares the date and time “{ref.key}” with something that is not a fixed date "
                             "and time with its offset, such as date-time('2026-03-15T14:30+05:30')")
    if vt == "time":
        if node.kind == "call" and node.value == "decimal-time" and len(node.args) == 1:
            inner = bare(node.args[0])
            m = _TIME.match(inner.value) if inner.kind == "str" else None
            if m:
                return m.group(1)
        raise Untranslatable(f"it compares the time “{ref.key}” without decimal-time() on both sides")
    if node.kind == "num" and vt in ("text", "choice"):
        node = Node("str", number_text(node.value))
    if node.kind != "str":
        raise Untranslatable(f"it compares “{ref.key}” with {_describe(node)}, not with a fixed value")
    if vt == "text":
        if op not in ("=", "!="):
            raise Untranslatable(f"it puts the text answer “{ref.key}” in order, which the form format cannot do")
        return node.value
    if vt == "choice":
        if op not in ("=", "!="):
            raise Untranslatable(f"it puts the choice “{ref.key}” in order, which the form format cannot do")
        if node.value not in (ref.choices or {}):
            raise Untranslatable(f"it compares “{ref.key}” with “{node.value}”, which is not one of its choices")
        return ref.choices[node.value]
    raise Untranslatable(f"it compares “{ref.key}” with a value, which the form format can do only for text, "
                         "numbers, dates, times and choices")


def _is_empty_text(node):
    node = bare(node)
    return node.kind == "str" and node.value == ""


def _is_number(node, *values):
    node = bare(node)
    return node.kind == "num" and node.value in values


def _comparison(node, res):
    op = node.value
    left, right = bare(node.args[0]), bare(node.args[1])
    if left.kind in ("str", "num", "neg") or (left.kind == "call" and left.value in ("date", "date-time")):
        left, right, op = right, left, _FLIP[op]
    if left.kind == "call" and left.value == "decimal-time" and len(left.args) == 1:
        ref = res.answer(left.args[0])
        if ref.value_type != "time":
            raise Untranslatable(f"it uses decimal-time() on “{ref.key}”, which is not a time")
    elif left.kind == "call" and left.value in ("string-length", "count-selected") and len(left.args) == 1:
        ref = res.answer(left.args[0])
        if (op == ">" and _is_number(right, 0)) or (op == ">=" and _is_number(right, 1)) or \
                (op == "!=" and _is_number(right, 0)):
            return {"op": "answered", "field": ref.key}
        if op == "=" and _is_number(right, 0):
            return {"op": "not", "condition": {"op": "answered", "field": ref.key}}
        raise Untranslatable(f"it tests the length of “{ref.key}”, which the form format can only do inside "
                             "the question's own limits")
    else:
        ref = res.answer(left)
    if _is_empty_text(right) and op in ("=", "!="):
        answered = {"op": "answered", "field": ref.key}
        return answered if op == "!=" else {"op": "not", "condition": answered}
    if ref.value_type == "choices":
        raise Untranslatable(f"it compares the multiple choice “{ref.key}” as a whole; use selected() to test "
                             "one choice")
    value = _literal(right, ref, op)
    if op == "!=":
        # In XLSForm != is true for an unanswered question, so it is not() of equals.
        return {"op": "not", "condition": {"op": "equals", "field": ref.key, "value": value}}
    return {"op": _COMPARE[op], "field": ref.key, "value": value}


def _simplify_not_equals(conditions):
    """(${f} != '' and ${f} != v) is how the form format's not-equals is written."""
    if len(conditions) == 2:
        first, second = conditions
        if first["op"] == "answered" and second["op"] == "not" and second["condition"]["op"] == "equals" \
                and second["condition"]["field"] == first["field"]:
            return {"op": "not-equals", "field": first["field"], "value": second["condition"]["value"]}
    return None


def to_condition(text, res):
    """Translate a relevant or required expression into a form-format condition."""
    return _condition(parse(text), res)


def _condition(node, res):
    if node.kind == "paren":
        return _condition(node.args[0], res)
    if node.kind == "bin" and node.value in ("and", "or"):
        parts = [_condition(part, res) for part in _chain(node, node.value)]
        if node.value == "and":
            simple = _simplify_not_equals(parts)
            if simple:
                return simple
        return {"op": "all" if node.value == "and" else "any", "conditions": parts}
    if node.kind == "bin" and node.value in _FLIP:
        return _comparison(node, res)
    if node.kind == "call" and node.value == "not" and len(node.args) == 1:
        return {"op": "not", "condition": _condition(node.args[0], res)}
    if node.kind == "call" and node.value == "selected" and len(node.args) == 2:
        ref = res.answer(node.args[0])
        value = bare(node.args[1])
        if ref.value_type not in ("choice", "choices"):
            raise Untranslatable(f"it uses selected() on “{ref.key}”, which is not a choice question")
        if value.kind == "num":
            value = Node("str", number_text(value.value))
        if value.kind != "str" or value.value not in (ref.choices or {}):
            raise Untranslatable(f"it tests “{ref.key}” for a value that is not one of its choices")
        op = "includes" if ref.value_type == "choices" else "equals"
        return {"op": op, "field": ref.key, "value": ref.choices[value.value]}
    if node.kind == "ref":
        ref = res.answer(node)
        return {"op": "answered", "field": ref.key}
    raise Untranslatable(f"it uses {_describe(node)}, which the form format has no condition for")


# ---------------------------------------------------------------------------
# Calculations

_NUMBER_SOURCES = {"number", "calculated", "fire-power"}
_HOURS_SOURCES = {"date-time", "observed-at", "raised-at"}


def to_calculation(text, res):
    """Translate an XLSForm calculation. Returns (calculation, decimals or None)."""
    node = bare(parse(text))
    decimals = None
    if node.kind == "call" and node.value == "round" and len(node.args) == 2 and \
            bare(node.args[1]).kind == "num" and bare(node.args[1]).value in range(7):
        decimals = bare(node.args[1]).value
        node = node.args[0]
    return _calculation(node, res), decimals


def _calc_field(node, res, kinds, what, scoped=True):
    """A question a calculation reads. Scoped questions must have one answer from here."""
    ref = res.answer(node) if scoped else res.ref(node)
    if ref.item_type not in kinds:
        raise Untranslatable(f"it uses “{ref.key}”, which is not {what}")
    return ref


def _hours(node, res):
    """(decimal-date-time(${end}) - decimal-date-time(${start})) * 24, in either order."""
    node = bare(node)
    if node.kind != "bin" or node.value != "*":
        return None
    left, right = bare(node.args[0]), bare(node.args[1])
    if _is_number(left, 24):
        left, right = right, left
    if not _is_number(right, 24) or left.kind != "bin" or left.value != "-":
        return None
    end, start = (bare(a) for a in left.args)
    if not all(a.kind == "call" and a.value == "decimal-date-time" and len(a.args) == 1 for a in (end, start)):
        return None
    end_ref = _calc_field(end.args[0], res, _HOURS_SOURCES, "a date and time")
    start_ref = _calc_field(start.args[0], res, _HOURS_SOURCES, "a date and time")
    return {"op": "hours-between", "start": start_ref.key, "end": end_ref.key}


def _scaled(node, func, divisor):
    """area(${f}) div 10000 and distance(${f}) div 1000 give hectares and kilometres."""
    node = bare(node)
    if node.kind == "bin" and node.value == "div" and _is_number(node.args[1], divisor):
        call = bare(node.args[0])
        if call.kind == "call" and call.value == func and len(call.args) == 1:
            return call.args[0]
    return None


_SHAPES = {"area": ("area", "polygon", 10000, "an area drawn on the map"),
           "distance": ("length", "track", 1000, "a walked track")}


def _calculation(node, res):
    hours = _hours(node, res)
    if hours:
        return hours
    for func, (op, item_type, divisor, what) in _SHAPES.items():
        inner = _scaled(node, func, divisor)
        if inner is not None:
            return {"op": op, "field": _calc_field(inner, res, {item_type}, what).key}
    if node.kind == "paren":
        return _calculation(node.args[0], res)
    if node.kind == "num":
        return {"op": "number", "value": node.value}
    if node.kind == "neg":
        inner = bare(node.args[0])
        if inner.kind == "num":
            return {"op": "number", "value": -inner.value}
        return {"op": "multiply", "args": [{"op": "number", "value": -1}, _calculation(node.args[0], res)]}
    if node.kind == "ref":
        return {"op": "field", "field": _calc_field(node, res, _NUMBER_SOURCES, "a number").key}
    if node.kind == "bin" and node.value in ("+", "*"):
        args = [_calculation(part, res) for part in _chain(node, node.value)]
        return {"op": "add" if node.value == "+" else "multiply", "args": args}
    if node.kind == "bin" and node.value in ("-", "div"):
        args = [_calculation(part, res) for part in node.args]
        return {"op": "subtract" if node.value == "-" else "divide", "args": args}
    if node.kind == "call" and node.value == "sum" and len(node.args) == 1:
        ref = _calc_field(node.args[0], res, _NUMBER_SOURCES, "a number", scoped=False)
        if not ref.repeats:
            raise Untranslatable(f"it adds up “{ref.key}”, which is not inside a repeating group")
        return {"op": "sum", "group": ref.repeats[-1], "field": ref.key}
    if node.kind == "call" and node.value == "count" and len(node.args) == 1:
        ref = res.ref(node.args[0])
        if ref.value_type != "repeat":
            raise Untranslatable(f"it counts “{ref.key}”, which is not a repeating group")
        return {"op": "count", "group": ref.key}
    if node.kind == "call" and node.value in _SHAPES and len(node.args) == 1:
        # Square metres and metres: the form format's area and length are in hectares and kilometres.
        op, item_type, scale, what = _SHAPES[node.value]
        ref = _calc_field(node.args[0], res, {item_type}, what)
        return {"op": "multiply", "args": [{"op": op, "field": ref.key}, {"op": "number", "value": scale}]}
    if node.kind == "bin" and (node.value in _FLIP or node.value in ("and", "or")):
        raise Untranslatable("it gives true or false, and a calculated field in the form format gives a number")
    if node.kind == "bin" and node.value == "mod":
        raise Untranslatable("it uses mod, which the form format has no calculation for")
    raise Untranslatable(f"it uses {_describe(node)}, which the form format has no calculation for")


# ---------------------------------------------------------------------------
# Constraints


@dataclass
class ConstraintPart:
    """A part of a constraint that was not translated, and why."""
    text: str
    reason: str


# Checks on the answer's length or count: (function on ".", item type) -> (lower, upper) property.
_LIMITS = {
    ("string-length", "text"): ("minLength", "maxLength"),
    ("count-selected", "multiple-choice"): ("minSelected", "maxSelected"),
}


def to_constraint(text, item_type, whole_numbers=False):
    """Translate a constraint on a question of item_type into field properties.

    Returns (properties, parts that could not be translated). Each part of a
    top-level "and" is translated on its own, so one odd part does not lose
    the others. whole_numbers says the question takes whole numbers only.
    """
    props, left, parts = {}, [], []

    def collect(node):
        if node.kind == "paren":
            collect(node.args[0])
        elif node.kind == "bin" and node.value == "and":
            collect(node.args[0])
            collect(node.args[1])
        else:
            parts.append(node)
    collect(parse(text))
    for node in parts:
        try:
            for name, value in _constraint_part(node, item_type, whole_numbers).items():
                if name in props:
                    raise Untranslatable(f"it sets the same limit as another part of the constraint")
                props[name] = value
        except Untranslatable as exc:
            left.append(ConstraintPart(write_back(node), str(exc)))
    return props, left


def _constraint_part(node, item_type, whole_numbers):
    node = bare(node)
    if node.kind == "call" and node.value == "regex" and len(node.args) == 2 and bare(node.args[0]).kind == "dot":
        pattern = bare(node.args[1])
        if item_type != "text" or pattern.kind != "str":
            raise Untranslatable("it matches a pattern, which the form format allows only on text questions")
        return {"pattern": _whole_match(pattern.value)}
    if node.kind == "bin" and node.value in ("=", "!="):
        raise Untranslatable("it allows or refuses one particular value, and the form format checks only a "
                             "smallest and largest value, a length, a count or a pattern")
    if node.kind != "bin" or node.value not in ("<", "<=", ">", ">="):
        raise Untranslatable(f"it uses {_describe(node)}, which the form format has no check for")
    op = node.value
    left, right = bare(node.args[0]), bare(node.args[1])
    if left.kind != "dot" and not (left.kind == "call" and len(left.args) == 1 and bare(left.args[0]).kind == "dot"):
        left, right, op = right, left, _FLIP[op]
    if right.kind == "neg" and bare(right.args[0]).kind == "num":
        right = Node("num", -bare(right.args[0]).value)
    if left.kind == "dot" and right.kind == "call" and not right.args:
        if op == "<=" and (item_type, right.value) in (("date", "today"), ("date-time", "now")):
            return {"notInFuture": True}
        raise Untranslatable(f"it compares the answer with {right.value}(), which the form format allows only as "
                             "“not in the future”: . <= today() on a date, or . <= now() on a date and time")
    if right.kind != "num":
        raise Untranslatable(f"it compares the answer with {_describe(right)}, not with a fixed number")
    if left.kind == "dot":
        if item_type != "number":
            raise Untranslatable("it compares the answer with a number, which the form format allows only on numbers")
        names, whole = ("min", "max"), whole_numbers
    else:
        if (left.value, item_type) not in _LIMITS:
            raise Untranslatable(f"it uses {_describe(left)}, which the form format has no check for here")
        names, whole = _LIMITS[(left.value, item_type)], True
        if not isinstance(right.value, int):
            raise Untranslatable("it compares a count with a number that is not whole")
    value = right.value
    if op in ("<", ">"):
        if not (whole and isinstance(value, int)):
            raise Untranslatable("it leaves out the limit itself, and the form format's limits always include it")
        value = value + 1 if op == ">" else value - 1
    if names[0] != "min" and value < 0:
        value = 0
    return {names[0] if op in (">", ">=") else names[1]: value}


def _whole_match(pattern):
    """ODK finds a pattern anywhere in the answer; the form format matches it as a whole."""
    if pattern.startswith("^") and pattern.endswith("$") and not pattern.endswith("\\$"):
        inner = pattern[1:-1]
        if inner.startswith("(") and inner.endswith(")") and _depths(inner[1:-1]) is not None:
            return inner[1:-1]
        if _depths(inner) == set():
            return inner
    raise Untranslatable("the pattern does not start with ^ and end with $ around the whole of it, so it does not "
                         "have to match the whole answer as the form format's patterns do")


def _depths(text):
    """The bracket depths at which | appears, or None if the brackets do not balance."""
    depth, escaped, in_class, bars = 0, False, False, set()
    for ch in text:
        if escaped:
            escaped = False
        elif ch == "\\":
            escaped = True
        elif in_class:
            in_class = ch != "]"
        elif ch == "[":
            in_class = True
        elif ch == "(":
            depth += 1
        elif ch == ")":
            depth -= 1
            if depth < 0:
                return None
        elif ch == "|":
            bars.add(depth)
    if depth:
        return None
    return {d for d in bars if d == 0}


def write_back(node):
    """Write a parsed expression as text again, for the review report."""
    kind = node.kind
    if kind == "ref":
        return "${" + node.value + "}"
    if kind == "dot":
        return "."
    if kind == "str":
        return _quote(node.value)
    if kind == "num":
        return number_text(node.value)
    if kind == "name":
        return node.value
    if kind == "paren":
        return "(" + write_back(node.args[0]) + ")"
    if kind == "neg":
        return "-" + write_back(node.args[0])
    if kind == "call":
        return node.value + "(" + ", ".join(write_back(a) for a in node.args) + ")"
    return f"{write_back(node.args[0])} {node.value} {write_back(node.args[1])}"


# ---------------------------------------------------------------------------
# Writing XPath


def number_text(value):
    if isinstance(value, bool):
        raise ValueError("not a number")
    if isinstance(value, int) or (isinstance(value, float) and value.is_integer() and abs(value) < 1e15):
        return str(int(value))
    return format(Decimal(repr(value)), "f")


def _quote(text):
    if "'" not in text:
        return f"'{text}'"
    if '"' not in text:
        return f'"{text}"'
    raise ValueError("a value cannot hold both kinds of quotation mark in XLSForm")


def _value_text(value, ref):
    vt = ref.value_type
    if vt == "number":
        return number_text(value)
    if vt == "date":
        return f"date({_quote(value)})"
    if vt == "date-time":
        return f"date-time({_quote(value)})"
    return _quote(value)


def condition_text(cond, refs):
    """Write a form-format condition as an XLSForm expression. refs maps keys to Ref."""
    op = cond["op"]
    if op in ("all", "any"):
        joiner = " and " if op == "all" else " or "
        return joiner.join(_wrapped(c, refs) for c in cond["conditions"])
    if op == "not":
        return f"not({condition_text(cond['condition'], refs)})"
    field = "${" + cond["field"] + "}"
    if op == "answered":
        return f"{field} != ''"
    ref = refs[cond["field"]]
    if op == "includes":
        return f"selected({field}, {_quote(cond['value'])})"
    if ref.value_type == "time":
        sign = {"equals": "=", "not-equals": "!=", "more-than": ">", "at-least": ">=",
                "less-than": "<", "at-most": "<="}[op]
        text = f"decimal-time({field}) {sign} decimal-time({_quote(cond['value'] + ':00')})"
        return f"({field} != '' and {text})" if op == "not-equals" else text
    value = _value_text(cond["value"], ref)
    if op == "not-equals":
        return f"({field} != '' and {field} != {value})"
    sign = {"equals": "=", "more-than": ">", "at-least": ">=", "less-than": "<", "at-most": "<="}[op]
    return f"{field} {sign} {value}"


def _wrapped(cond, refs):
    text = condition_text(cond, refs)
    return f"({text})" if cond["op"] in ("all", "any") else text


def calculation_text(calc):
    """Write a form-format calculation as an XLSForm expression."""
    op = calc["op"]
    if op == "number":
        return number_text(calc["value"])
    if op == "field":
        return "${" + calc["field"] + "}"
    if op in ("add", "multiply", "subtract", "divide"):
        sign = {"add": " + ", "multiply": " * ", "subtract": " - ", "divide": " div "}[op]
        return sign.join(_calc_wrapped(a) for a in calc["args"])
    if op == "sum":
        return "sum(${" + calc["field"] + "})"
    if op == "count":
        return "count(${" + calc["group"] + "})"
    if op == "area":
        return "(area(${" + calc["field"] + "}) div 10000)"
    if op == "length":
        return "(distance(${" + calc["field"] + "}) div 1000)"
    if op == "hours-between":
        return "((decimal-date-time(${" + calc["end"] + "}) - decimal-date-time(${" + calc["start"] + "})) * 24)"
    raise ValueError(f"unknown calculation {op}")


def _calc_wrapped(calc):
    text = calculation_text(calc)
    if calc["op"] in ("add", "multiply", "subtract", "divide") or \
            (calc["op"] == "number" and calc["value"] < 0):
        return f"({text})"
    return text


def pattern_text(pattern):
    """The form format matches a pattern as a whole; ODK finds it anywhere."""
    if _depths(pattern) != set():
        return f"regex(., {_quote('^(' + pattern + ')$')})"
    return f"regex(., {_quote('^' + pattern + '$')})"
