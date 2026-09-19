package main

type myErr string

func (e myErr) Error() string { return "myErr: " + string(e) }

func safe(f func()) (err any) {
	defer func() {
		if r := recover(); r != nil {
			err = r
		}
	}()
	f()
	return nil
}

func divide(a, b int) (q int, err any) {
	defer func() { err = recover() }()
	return a / b, nil
}

func namedResult() (s string) {
	defer func() {
		if recover() != nil {
			s = "recovered"
		}
	}()
	panic("inside")
}

func nested() string {
	defer func() { recover() }()
	defer println("inner defer still runs")
	panic("nested panic")
}

func rethrow() {
	defer func() {
		if r := recover(); r != nil {
			panic("second: " + r.(string))
		}
	}()
	panic("first")
}

func noPanic() any {
	defer func() {}()
	return recover() // nil: nothing is panicking
}

func main() {
	// A recovered string panic.
	e := safe(func() { panic("boom") })
	println(e != nil, e.(string))

	// A recovered runtime error satisfies error.
	e2 := safe(func() { var p *int; _ = *p })
	err, ok := e2.(error)
	println(ok, err.Error())

	// Recovering a custom error value.
	e3 := safe(func() { panic(myErr("custom")) })
	me, ok3 := e3.(myErr)
	println(ok3, me.Error())

	// Index out of range, recovered.
	e4 := safe(func() { a := []int{1}; _ = a[5] })
	println(e4.(error).Error())

	// Named results set by the deferred call.
	q, err5 := divide(10, 0)
	println(q, err5 != nil)
	q2, err6 := divide(10, 2)
	println(q2, err6 == nil)
	println(namedResult())
	println(nested())
	println(noPanic() == nil)

	// A panic raised inside a recover, caught outside.
	e7 := safe(rethrow)
	println(e7.(string))

	// Without a recover, the panic still gets out.
	defer println("main's defer")
	panic(myErr("final"))
}
