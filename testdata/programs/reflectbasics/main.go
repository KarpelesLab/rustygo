package main

import "reflect"

type Point struct {
	X, Y int
	Name string `json:"name" db:"nm"`
	tags []string
}

type Celsius float64

func main() {
	p := Point{1, 2, "origin", []string{"a"}}
	t := reflect.TypeOf(p)
	println(t.Kind().String(), t.Name(), t.String(), t.NumField(), int(t.Size()))
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		println(" field", f.Name, f.Type.String(), int(f.Offset), f.Tag.Get("json"), f.IsExported())
	}
	v := reflect.ValueOf(p)
	println(v.Kind().String(), int(v.Field(0).Int()), v.Field(2).String(), v.NumField())

	var c Celsius = 36.6
	cv := reflect.ValueOf(c)
	println(cv.Kind().String(), cv.Type().String(), cv.Type().Name(), cv.Float() > 36.0)

	xs := []int{10, 20, 30}
	sv := reflect.ValueOf(xs)
	println(sv.Kind().String(), sv.Len(), int(sv.Index(1).Int()), sv.Type().Elem().String())

	arr := [2]string{"a", "b"}
	av := reflect.ValueOf(arr)
	println(av.Kind().String(), av.Len(), av.Index(1).String(), av.Type().Len())

	m := map[string]int{"one": 1, "two": 2}
	mv := reflect.ValueOf(m)
	println(mv.Kind().String(), mv.Len(), int(mv.MapIndex(reflect.ValueOf("two")).Int()))
	total := 0
	it := mv.MapRange()
	for it.Next() {
		total += int(it.Value().Int()) + len(it.Key().String())
	}
	println("map total", total, len(mv.MapKeys()))

	ptr := &p
	pv := reflect.ValueOf(ptr)
	println(pv.Kind().String(), pv.IsNil(), pv.Elem().Kind().String(), int(pv.Elem().Field(1).Int()))

	var nilp *Point
	println(reflect.ValueOf(nilp).IsNil(), reflect.ValueOf(nilp).Kind().String())

	var any1 any = 42
	av2 := reflect.ValueOf(any1)
	println(av2.Kind().String(), int(av2.Int()), av2.Interface().(int) == 42)

	println(reflect.DeepEqual(p, Point{1, 2, "origin", []string{"a"}}), reflect.DeepEqual(xs, []int{10, 20, 31}))
	println(reflect.TypeOf(3.5).Kind().String(), reflect.TypeOf("s").Kind().String(), reflect.TypeOf(true).Comparable())
	println(reflect.ValueOf(uint8(255)).Uint() == 255, reflect.ValueOf(complex(1, 2)).Complex() == complex(1, 2))
}
