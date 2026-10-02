// Package quickjs implements only the reviewed reactor ABI. All VM methods and
// callbacks run on its owner goroutine; hosts never retain or touch VM handles.
package quickjs

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

type key struct{}
type Fault struct{ Err error }

func (e *Fault) Error() string { return e.Err.Error() }
func (e *Fault) Unwrap() error { return e.Err }

// Host returns a JSON response string. Arguments are length-aware copied strings.
type Host func(op string, args []string) string

type VM struct {
	Module      api.Module
	ctx         context.Context
	host        Host
	maxString   int
	stringLimit func(op string, index int) int
	rejections  map[uint64]uint64
	functions   map[string]api.Function
}

// RegisterImports accepts exactly the imports present in the pinned artifact.
// WASI descriptors are unavailable; only clock/random primitives are provided.
func RegisterImports(ctx context.Context, rt wazero.Runtime, compiled wazero.CompiledModule) error {
	allowed := map[string]map[string]bool{
		"env":                    {"host_call": true, "host_interrupt": true, "host_promise_rejection": true, "host_module_normalize": true, "host_module_load": true, "host_get_timezone_offset": true},
		"wasi_snapshot_preview1": {"clock_time_get": true, "fd_close": true, "fd_fdstat_get": true, "fd_seek": true, "fd_write": true, "random_get": true},
	}
	builders := map[string]wazero.HostModuleBuilder{}
	for _, imp := range compiled.ImportedFunctions() {
		module, name, _ := imp.Import()
		if !allowed[module][name] {
			return fmt.Errorf("unexpected WASM import %s.%s", module, name)
		}
		b := builders[module]
		if b == nil {
			b = rt.NewHostModuleBuilder(module)
			builders[module] = b
		}
		b.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
			v, _ := ctx.Value(key{}).(*VM)
			switch name {
			case "host_call":
				if v == nil {
					panic(&Fault{fmt.Errorf("missing VM owner")})
				}
				argc, argv := int(stack[3]), uint32(stack[4])
				if argc < 1 || argc > 4 {
					panic(&Fault{fmt.Errorf("invalid host argument count")})
				}
				args := make([]string, argc)
				for i := range args {
					h, ok := m.Memory().ReadUint32Le(argv + uint32(i)*4)
					if !ok {
						panic(&Fault{fmt.Errorf("invalid host argv")})
					}
					limit := 32
					if i > 0 {
						limit = v.maxString
						if v.stringLimit != nil {
							limit = v.stringLimit(args[0], i-1)
						}
					}
					args[i] = v.String(uint64(h), limit)
				}
				response := v.host(args[0], args[1:])
				stack[0] = v.NewString(response)
			case "host_interrupt":
				stack[0] = 0
				if ctx.Err() != nil {
					stack[0] = 1
				}
			case "host_promise_rejection":
				if v == nil {
					panic(&Fault{fmt.Errorf("missing VM owner")})
				}
				promise, reason, handled := stack[0], stack[1], stack[2]
				identity := v.Call("qjs_get_value_ptr", promise)
				if old := v.rejections[identity]; old != 0 {
					v.Free(old)
					delete(v.rejections, identity)
				}
				if handled == 0 {
					if len(v.rejections) >= 128 {
						v.Free(promise)
						v.Free(reason)
						panic(&Fault{fmt.Errorf("unhandled rejection bound exceeded")})
					}
					v.rejections[identity] = v.Call("qjs_dup_value", reason)
				}
				v.Free(promise)
				v.Free(reason)
			case "host_module_normalize", "host_module_load", "host_get_timezone_offset":
				// No loader is installed and UTC does not expose host environment.
				stack[0] = 0
			case "clock_time_get":
				if !m.Memory().WriteUint64Le(uint32(stack[2]), uint64(time.Now().UnixNano())) {
					stack[0] = 21
				} else {
					stack[0] = 0
				}
			case "random_get":
				buf, ok := m.Memory().Read(uint32(stack[0]), uint32(stack[1]))
				if !ok {
					stack[0] = 21
					return
				}
				if _, err := rand.Read(buf); err != nil {
					stack[0] = 29
				} else {
					stack[0] = 0
				}
			default:
				stack[0] = 8 // WASI EBADF: no stdin/stdout/file descriptors.
			}
		}), imp.ParamTypes(), imp.ResultTypes()).Export(name)
	}
	for _, b := range builders {
		if _, err := b.Instantiate(ctx); err != nil {
			return err
		}
	}
	return nil
}

func New(ctx context.Context, rt wazero.Runtime, compiled wazero.CompiledModule, name string, heap, stack uint64, maxString int, host Host, stringLimit func(string, int) int) (v *VM, err error) {
	v = &VM{ctx: ctx, host: host, maxString: maxString, stringLimit: stringLimit, rejections: map[uint64]uint64{}, functions: map[string]api.Function{}}
	v.ctx = context.WithValue(ctx, key{}, v)
	defer Recover(&err)
	v.Module, err = rt.InstantiateModule(v.ctx, compiled, wazero.NewModuleConfig().WithName(name).WithStartFunctions("_initialize"))
	if err != nil {
		return nil, err
	}
	if v.Call("qjs_init") != 0 {
		return v, fmt.Errorf("QuickJS initialization failed")
	}
	v.Call("qjs_set_memory_limit", heap)
	v.Call("qjs_set_max_stack_size", stack)
	v.Call("qjs_set_interrupt_handler", 1)
	v.Call("qjs_set_promise_rejection_handler", 1)
	n := v.put("__pigo_host")
	fn := v.Call("qjs_new_host_function", n, 11, 4)
	g := v.Call("qjs_get_global")
	v.Call("qjs_set_prop_string", g, n, fn)
	v.Call("wasm_free", n)
	v.Free(fn)
	v.Free(g)
	return v, nil
}

// Recover converts internal ABI faults to errors at owner entry points. Other
// programming panics are also contained as a sandbox failure.
func Recover(err *error) {
	if p := recover(); p != nil {
		if e, ok := p.(error); ok {
			*err = e
		} else {
			*err = fmt.Errorf("QuickJS ABI fault: %v", p)
		}
	}
}

func (v *VM) Call(name string, args ...uint64) uint64 {
	f := v.functions[name]
	if f == nil {
		f = v.Module.ExportedFunction(name)
		if f == nil {
			panic(&Fault{fmt.Errorf("missing export %s", name)})
		}
		v.functions[name] = f
	}
	r, err := f.Call(v.ctx, args...)
	if err != nil {
		panic(&Fault{err})
	}
	if len(r) == 0 {
		return 0
	}
	return r[0]
}

func (v *VM) put(s string) uint64 {
	p := v.Call("wasm_malloc", uint64(len(s)+1))
	if p == 0 {
		panic(&Fault{fmt.Errorf("guest allocation failed")})
	}
	if !v.Module.Memory().WriteString(uint32(p), s) || !v.Module.Memory().WriteByte(uint32(p)+uint32(len(s)), 0) {
		panic(&Fault{fmt.Errorf("invalid guest write")})
	}
	return p
}

func (v *VM) NewString(s string) uint64 {
	p := v.put(s)
	h := v.Call("qjs_new_string", p, uint64(len(s)))
	v.Call("wasm_free", p)
	return h
}
func (v *VM) Free(h uint64) {
	if h != 0 {
		v.Call("qjs_free_value", h)
	}
}

// String uses explicit bytes and never treats embedded NUL as a terminator.
// The byte length is checked before any copy from guest memory.
func (v *VM) String(h uint64, max int) string {
	lp := v.Call("wasm_malloc", 4)
	if lp == 0 {
		panic(&Fault{fmt.Errorf("guest allocation failed")})
	}
	p := v.Call("qjs_get_string_len", h, lp)
	n, ok := v.Module.Memory().ReadUint32Le(uint32(lp))
	if !ok || p == 0 {
		panic(&Fault{fmt.Errorf("invalid guest string")})
	}
	if uint64(n) > uint64(max) {
		v.Call("qjs_free_cstring", p)
		v.Call("wasm_free", lp)
		panic(&Fault{fmt.Errorf("bridge string exceeds byte limit")})
	}
	b, ok := v.Module.Memory().Read(uint32(p), n)
	if !ok {
		panic(&Fault{fmt.Errorf("invalid guest string bytes")})
	}
	s := string(b)
	v.Call("qjs_free_cstring", p)
	v.Call("wasm_free", lp)
	return s
}

func (v *VM) Eval(source, filename string) uint64 {
	p := v.put(source)
	f := v.put(filename)
	h := v.Call("qjs_eval", p, uint64(len(source)), f, 0)
	v.Call("wasm_free", p)
	v.Call("wasm_free", f)
	if v.Call("qjs_is_exception", h) != 0 {
		v.Free(h)
		e := v.Call("qjs_get_exception")
		message := v.ErrorText(e)
		v.Free(e)
		panic(&Fault{fmt.Errorf("%s", message)})
	}
	return h
}

func (v *VM) Prop(h uint64, name string) uint64 {
	p := v.put(name)
	r := v.Call("qjs_get_prop_string", h, p)
	v.Call("wasm_free", p)
	return r
}

func (v *VM) Invoke(fn uint64, args ...uint64) uint64 {
	p := uint64(0)
	if len(args) > 0 {
		p = v.Call("wasm_malloc", uint64(len(args)*4))
		if p == 0 {
			panic(&Fault{fmt.Errorf("guest allocation failed")})
		}
		for i, h := range args {
			if !v.Module.Memory().WriteUint32Le(uint32(p)+uint32(i)*4, uint32(h)) {
				panic(&Fault{fmt.Errorf("invalid argument write")})
			}
		}
	}
	u := v.Call("qjs_get_undefined")
	r := v.Call("qjs_call", fn, u, uint64(len(args)), p)
	v.Free(u)
	if p != 0 {
		v.Call("wasm_free", p)
	}
	if v.Call("qjs_is_exception", r) != 0 {
		v.Free(r)
		e := v.Call("qjs_get_exception")
		message := v.ErrorText(e)
		v.Free(e)
		panic(&Fault{fmt.Errorf("%s", message)})
	}
	return r
}

func (v *VM) ErrorText(h uint64) string {
	if v.Call("qjs_typeof", h) == uint64(^uint32(0)) || int32(v.Call("qjs_typeof", h)) == -1 {
		st := v.Prop(h, "stack")
		if v.Call("qjs_is_undefined", st) == 0 {
			s := v.String(st, 8192)
			v.Free(st)
			return v.String(h, 8192) + "\n" + s
		}
		v.Free(st)
	}
	return v.String(h, 8192)
}

func (v *VM) PendingJobs() bool { return v.Call("qjs_is_job_pending") != 0 }
func (v *VM) Job() {
	if int32(v.Call("qjs_execute_pending_job")) < 0 {
		e := v.Call("qjs_get_exception")
		s := v.ErrorText(e)
		v.Free(e)
		panic(&Fault{fmt.Errorf("pending job: %s", s)})
	}
}
func (v *VM) Rejections() []uint64 {
	r := make([]uint64, 0, len(v.rejections))
	for _, h := range v.rejections {
		r = append(r, h)
	}
	return r
}

func (v *VM) Close() {
	if v != nil && v.Module != nil {
		_ = v.Module.Close(context.Background())
	}
}

func IsCancellation(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "context canceled") || strings.Contains(err.Error(), "deadline exceeded"))
}
