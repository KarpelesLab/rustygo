package main

type Shape interface {
	Area() int
	Name() string
}

type Rect struct{ W, H int }
type Square struct{ S int }

func (r Rect) Area() int       { return r.W * r.H }
func (r Rect) Name() string    { return "rect" }
func (s *Square) Area() int    { return s.S * s.S }
func (s *Square) Name() string { return "square" }

type Stringer interface{ String() string }

type Temp int

func (t Temp) String() string { return "temp:" + itoa(int(t)) }

type myErr string

func (e myErr) Error() string { return "err: " + string(e) }

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

func describe(v any) string {
	switch x := v.(type) {
	case int:
		return "int " + itoa(x)
	case string:
		return "string " + x
	case bool:
		if x {
			return "bool true"
		}
		return "bool false"
	case Rect:
		return "rect " + itoa(x.Area())
	case Shape:
		return "shape " + x.Name()
	case error:
		return "error " + x.Error()
	case nil:
		return "nil"
	}
	return "other"
}

func total(shapes []Shape) int {
	t := 0
	for _, s := range shapes {
		t += s.Area()
	}
	return t
}

func main() {
	// Dynamic dispatch, value and pointer receivers.
	shapes := []Shape{Rect{2, 3}, &Square{4}, Rect{1, 1}}
	for _, s := range shapes {
		println(s.Name(), s.Area())
	}
	println("total", total(shapes))

	// Interface values in variables, compared and reassigned.
	var s Shape
	println(s == nil)
	s = Rect{5, 5}
	println(s != nil, s.Area(), s == Shape(Rect{5, 5}), s == Shape(Rect{5, 6}))

	// any, type assertions, comma-ok.
	var v any = 42
	n, ok := v.(int)
	println(n, ok)
	_, bad := v.(string)
	println(bad)
	println(v.(int) + 1)

	// Assertion to an interface type.
	var a any = &Square{3}
	sh, ok2 := a.(Shape)
	println(ok2, sh.Area())
	_, no := a.(Stringer)
	println(no)

	// Type switches over many shapes.
	println(describe(7), describe("hi"), describe(true))
	println(describe(Rect{3, 3}), describe(&Square{2}), describe(nil))
	println(describe(myErr("bad")))

	// Stringer and error through interfaces.
	var st Stringer = Temp(21)
	println(st.String())
	var e error = myErr("oops")
	println(e.Error())

	// Interfaces holding interfaces, and nil checks after assignment.
	var any2 any = st
	st2, ok3 := any2.(Stringer)
	println(ok3, st2.String())

	// Interface method value through a variable.
	f := e.Error
	println(f())

	// Failed assertion panics with gc's message.
	defer func() { println("deferred after panic") }()
	println(v.(string))
}
