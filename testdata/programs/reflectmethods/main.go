// Program reflectmethods asks reflection for a type's methods: the method
// expression `T.M`, whose first argument is the receiver, and the method value
// `x.M`, which carries the receiver with it. Both come back as func values,
// and asserting each to the func type the program wrote is the point — it only
// succeeds if the descriptor reflection attached is the same one the compiler
// would have used.
package main

import (
	"fmt"
	"reflect"
	"sort"
)

type counter struct {
	n    int
	name string
}

func (c counter) Name() string        { return c.name }
func (c counter) Plus(d int) int      { return c.n + d }
func (c counter) Pair() (int, string) { return c.n, c.name }
func (c *counter) Add(d int)          { c.n += d }
func (c counter) hidden() int         { return c.n } // unexported: reflection skips it

type named int

func (n named) Twice() named { return n * 2 }

func main() {
	c := counter{n: 10, name: "ten"}
	t := reflect.TypeOf(c)
	fmt.Println(t.String(), t.NumMethod())

	// The methods, in the order reflection reports them.
	var names []string
	for i := 0; i < t.NumMethod(); i++ {
		m := t.Method(i)
		names = append(names, fmt.Sprintf("%d:%s:%s:%v", m.Index, m.Name, m.Type, m.IsExported()))
	}
	fmt.Println(names)
	if !sort.StringsAreSorted(sortableNames(t)) {
		fmt.Println("methods are not in order")
	}

	// A method expression, called with the receiver.
	name := t.Method(indexOf(t, "Name")).Func.Interface().(func(counter) string)
	fmt.Println(name(c), name(counter{name: "other"}))

	plus := t.Method(indexOf(t, "Plus")).Func.Interface().(func(counter, int) int)
	fmt.Println(plus(c, 5), plus(counter{n: 1}, 2))

	pair := t.Method(indexOf(t, "Pair")).Func.Interface().(func(counter) (int, string))
	n, s := pair(c)
	fmt.Println(n, s)

	// A method value, which already knows its receiver.
	v := reflect.ValueOf(c)
	fmt.Println(v.NumMethod())
	fmt.Println(v.Method(indexOf(t, "Name")).Interface().(func() string)())
	fmt.Println(v.MethodByName("Plus").Interface().(func(int) int)(7))

	// By name, and a name that is not there.
	if m, ok := t.MethodByName("Pair"); ok {
		fmt.Println("found", m.Name, m.Type, m.Index)
	}
	if _, ok := t.MethodByName("hidden"); ok {
		fmt.Println("hidden should not be reported")
	}
	if _, ok := t.MethodByName("Add"); ok {
		fmt.Println("Add should not be a method of counter")
	}

	// A pointer receiver: its method set has both.
	pt := reflect.TypeOf(&c)
	fmt.Println(pt.NumMethod())
	add := pt.Method(indexOf(pt, "Add")).Func.Interface().(func(*counter, int))
	add(&c, 3)
	fmt.Println(c.n)
	reflect.ValueOf(&c).MethodByName("Add").Interface().(func(int))(4)
	fmt.Println(c.n)

	// A named non-struct type, and a method returning its own type.
	nt := reflect.TypeOf(named(3))
	twice := nt.Method(0).Func.Interface().(func(named) named)
	fmt.Println(twice(named(3)), nt.Method(0).Name, nt.NumMethod())

	// An interface's own method set is its declared methods.
	it := reflect.TypeOf((*fmt.Stringer)(nil)).Elem()
	fmt.Println(it.Kind(), it.NumMethod())
}

func indexOf(t reflect.Type, name string) int {
	m, ok := t.MethodByName(name)
	if !ok {
		panic("no method " + name)
	}
	return m.Index
}

func sortableNames(t reflect.Type) []string {
	var out []string
	for i := 0; i < t.NumMethod(); i++ {
		out = append(out, t.Method(i).Name)
	}
	return out
}
