// A small expression language: lexer, parser, evaluator and environment,
// written without the standard library. It leans on most of what M1 added.
package main

type Kind int

const (
	NUM Kind = iota
	IDENT
	OP
	LPAREN
	RPAREN
	COMMA
	EOF
)

type Token struct {
	Kind Kind
	Text string
	Num  int
}

type Lexer struct {
	src string
	pos int
}

func (l *Lexer) next() Token {
	for l.pos < len(l.src) && l.src[l.pos] == ' ' {
		l.pos++
	}
	if l.pos >= len(l.src) {
		return Token{Kind: EOF}
	}
	c := l.src[l.pos]
	switch {
	case c >= '0' && c <= '9':
		n := 0
		for l.pos < len(l.src) && l.src[l.pos] >= '0' && l.src[l.pos] <= '9' {
			n = n*10 + int(l.src[l.pos]-'0')
			l.pos++
		}
		return Token{Kind: NUM, Num: n}
	case c == '(':
		l.pos++
		return Token{Kind: LPAREN, Text: "("}
	case c == ')':
		l.pos++
		return Token{Kind: RPAREN, Text: ")"}
	case c == ',':
		l.pos++
		return Token{Kind: COMMA, Text: ","}
	case isLetter(c):
		start := l.pos
		for l.pos < len(l.src) && (isLetter(l.src[l.pos]) || (l.src[l.pos] >= '0' && l.src[l.pos] <= '9')) {
			l.pos++
		}
		return Token{Kind: IDENT, Text: l.src[start:l.pos]}
	}
	l.pos++
	return Token{Kind: OP, Text: string(rune(c))}
}

func isLetter(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_'
}

// Expressions form a small interface hierarchy.
type Expr interface {
	Eval(env *Env) int
	String() string
}

type Num struct{ Val int }
type Var struct{ Name string }
type Binary struct {
	Op   string
	L, R Expr
}
type Call struct {
	Fn   string
	Args []Expr
}

func (n Num) Eval(*Env) int  { return n.Val }
func (n Num) String() string { return itoa(n.Val) }

func (v Var) Eval(env *Env) int {
	x, ok := env.Lookup(v.Name)
	if !ok {
		panic("undefined variable: " + v.Name)
	}
	return x
}
func (v Var) String() string { return v.Name }

func (b Binary) Eval(env *Env) int {
	l, r := b.L.Eval(env), b.R.Eval(env)
	switch b.Op {
	case "+":
		return l + r
	case "-":
		return l - r
	case "*":
		return l * r
	case "/":
		return l / r
	case "%":
		return l % r
	}
	panic("bad operator: " + b.Op)
}
func (b Binary) String() string { return "(" + b.L.String() + b.Op + b.R.String() + ")" }

func (c Call) Eval(env *Env) int {
	fn, ok := env.Func(c.Fn)
	if !ok {
		panic("undefined function: " + c.Fn)
	}
	args := make([]int, 0, len(c.Args))
	for _, a := range c.Args {
		args = append(args, a.Eval(env))
	}
	return fn(args...)
}
func (c Call) String() string {
	s := c.Fn + "("
	for i, a := range c.Args {
		if i > 0 {
			s += ","
		}
		s += a.String()
	}
	return s + ")"
}

// Env chains scopes; funcs are variadic closures.
type Env struct {
	vars   map[string]int
	funcs  map[string]func(...int) int
	parent *Env
}

func NewEnv(parent *Env) *Env {
	return &Env{vars: map[string]int{}, funcs: map[string]func(...int) int{}, parent: parent}
}

func (e *Env) Lookup(name string) (int, bool) {
	for s := e; s != nil; s = s.parent {
		if v, ok := s.vars[name]; ok {
			return v, true
		}
	}
	return 0, false
}

func (e *Env) Func(name string) (func(...int) int, bool) {
	for s := e; s != nil; s = s.parent {
		if f, ok := s.funcs[name]; ok {
			return f, true
		}
	}
	return nil, false
}

type Parser struct {
	lex  *Lexer
	tok  Token
	prev Token
}

func NewParser(src string) *Parser {
	p := &Parser{lex: &Lexer{src: src}}
	p.advance()
	return p
}

func (p *Parser) advance() { p.prev, p.tok = p.tok, p.lex.next() }

func (p *Parser) Parse() Expr {
	e := p.parseBinary(0)
	if p.tok.Kind != EOF {
		panic("trailing input at " + itoa(p.lex.pos))
	}
	return e
}

func precedence(op string) int {
	switch op {
	case "+", "-":
		return 1
	case "*", "/", "%":
		return 2
	}
	return 0
}

func (p *Parser) parseBinary(min int) Expr {
	left := p.parseAtom()
	for p.tok.Kind == OP && precedence(p.tok.Text) >= min && precedence(p.tok.Text) > 0 {
		op := p.tok.Text
		prec := precedence(op)
		p.advance()
		right := p.parseBinary(prec + 1)
		left = Binary{Op: op, L: left, R: right}
	}
	return left
}

func (p *Parser) parseAtom() Expr {
	switch p.tok.Kind {
	case NUM:
		n := p.tok.Num
		p.advance()
		return Num{Val: n}
	case IDENT:
		name := p.tok.Text
		p.advance()
		if p.tok.Kind != LPAREN {
			return Var{Name: name}
		}
		p.advance()
		call := Call{Fn: name}
		for p.tok.Kind != RPAREN {
			call.Args = append(call.Args, p.parseBinary(0))
			if p.tok.Kind == COMMA {
				p.advance()
			}
		}
		p.advance()
		return call
	case LPAREN:
		p.advance()
		e := p.parseBinary(0)
		p.advance() // ')'
		return e
	}
	panic("unexpected token: " + p.tok.Text)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	if neg {
		s = "-" + s
	}
	return s
}

// evalSafe turns a panic into an error result, using defer and recover.
func evalSafe(src string, env *Env) (result int, err string) {
	defer func() {
		if r := recover(); r != nil {
			if s, ok := r.(string); ok {
				err = s
			} else if e, ok := r.(error); ok {
				err = e.Error()
			}
		}
	}()
	return NewParser(src).Parse().Eval(env), ""
}

func main() {
	global := NewEnv(nil)
	global.vars["x"] = 10
	global.vars["y"] = 4
	global.funcs["max"] = func(xs ...int) int {
		m := xs[0]
		for _, v := range xs[1:] {
			if v > m {
				m = v
			}
		}
		return m
	}
	global.funcs["sum"] = func(xs ...int) int {
		t := 0
		for _, v := range xs {
			t += v
		}
		return t
	}
	// A closure capturing the environment it was defined in.
	global.funcs["scaled"] = func(xs ...int) int {
		f, _ := global.Lookup("x")
		return xs[0] * f
	}

	inputs := []string{
		"1+2*3",
		"(1+2)*3",
		"x*y-2",
		"max(1, x, y, 7)",
		"sum(1,2,3) + max(x,y)",
		"scaled(3)",
		"100/y%3",
		"x/(y-4)",
		"nope+1",
		"bad(1)",
	}
	for _, src := range inputs {
		v, err := evalSafe(src, global)
		if err != "" {
			println(src, "=> error:", err)
		} else {
			println(src, "=>", v, "tree:", NewParser(src).Parse().String())
		}
	}

	// A child scope shadows its parent.
	child := NewEnv(global)
	child.vars["x"] = 100
	v, _ := evalSafe("x+y", child)
	println("child x+y =", v)

	// Exercise the collector: many short-lived trees.
	total := 0
	for i := 0; i < 2000; i++ {
		n, _ := evalSafe("1+2*"+itoa(i%50), global)
		total += n
	}
	println("total", total)
}
