package main

type Celsius float64
type Fahrenheit float64

func (c Celsius) ToF() Fahrenheit { return Fahrenheit(c*9/5 + 32) }

type Counter int

func (c *Counter) Inc() { *c++ }

func (c Counter) Double() Counter { return c * 2 }

func main() {
	var c Celsius = 100
	println(c.ToF(), Celsius(-40).ToF())
	var n Counter
	n.Inc()
	n.Inc()
	p := &n
	p.Inc()
	println(n, n.Double(), p.Double())
}
