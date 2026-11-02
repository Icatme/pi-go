package pigo

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestKiroHostOptionsRejectEveryUnmappedField(t *testing.T) {
	for _, simple := range []bool{false, true} {
		var options any = &ProviderStreamOptions{RequestContext: context.Background()}
		if simple {
			options = &SimpleStreamOptions{RequestContext: context.Background()}
		}
		base := reflect.ValueOf(options).Elem()
		for i := 0; i < base.NumField(); i++ {
			name := base.Type().Field(i).Name
			if name == "RequestContext" || name == "Transport" {
				continue
			}
			copy := reflect.New(base.Type()).Elem()
			copy.Set(base)
			field := copy.Field(i)
			switch field.Kind() {
			case reflect.String:
				field.SetString("unsupported")
			case reflect.Int:
				field.SetInt(1)
			case reflect.Map:
				field.Set(reflect.MakeMap(field.Type()))
			case reflect.Pointer:
				field.Set(reflect.New(field.Type().Elem()))
			case reflect.Func:
				field.Set(reflect.MakeFunc(field.Type(), func([]reflect.Value) []reflect.Value { t.Error("option callback invoked"); return nil }))
			case reflect.Interface:
				field.Set(reflect.ValueOf(&recordingObserver{}))
			case reflect.Struct:
				field.Field(0).SetInt(1)
			default:
				t.Fatalf("new option kind needs test fixture: %s %s", name, field.Kind())
			}
			var err error
			if simple {
				err = validateKiroHostSimpleOptions(Model{}, copy.Interface().(SimpleStreamOptions))
			} else {
				err = validateKiroHostOptions(Model{}, copy.Interface().(ProviderStreamOptions))
			}
			if err == nil || err.Error() != "kiro: unsupported host options" {
				t.Fatalf("unmapped %s accepted/leaked", name)
			}
		}
	}
}

func TestKiroHostOptionsContextTransportAndNoMutation(t *testing.T) {
	for _, transport := range []Transport{"", TransportSSE, TransportAuto, TransportWebSocket, "future"} {
		ctx, cancel := context.WithCancel(context.Background())
		simple := SimpleStreamOptions{RequestContext: ctx, Transport: transport}
		full := ProviderStreamOptions{RequestContext: ctx, Transport: transport}
		allowed := transport == "" || transport == TransportSSE
		if (validateKiroHostSimpleOptions(Model{}, simple) == nil) != allowed || (validateKiroHostOptions(Model{}, full) == nil) != allowed {
			t.Fatal("transport contract")
		}
		if simple.RequestContext != ctx || simple.Transport != transport {
			t.Fatal("mutated input")
		}
		cancel()
		if validateKiroHostSimpleOptions(Model{}, simple) == nil || validateKiroHostOptions(Model{}, full) == nil {
			t.Fatal("cancelled context")
		}
	}
	if validateKiroHostSimpleOptions(Model{}, SimpleStreamOptions{}) == nil || validateKiroHostOptions(Model{}, ProviderStreamOptions{}) == nil {
		t.Fatal("nil context")
	}
}

func TestAPIOptionPreflightBeforeClientAndDispatch(t *testing.T) {
	for _, simple := range []bool{false, true} {
		for _, reject := range []bool{false, true} {
			source := t.Name() + "/" + string(rune('a'+boolInt(simple)*2+boolInt(reject)))
			api := API(source)
			validations, dispatches := 0, 0
			client := &http.Client{}
			validate := func(observed *http.Client) error {
				validations++
				if observed != client {
					t.Error("client wrapped before preflight")
				}
				if reject {
					return errors.New("PRIVATE_OPTION_SECRET")
				}
				return nil
			}
			emit := func(observed *http.Client) *AssistantMessageEventStream {
				dispatches++
				if observed == nil || observed == client {
					t.Error("accepted callback did not receive usual client wrapping")
				}
				return streamAPIUnavailable(Model{API: api}, "fixture end")
			}
			RegisterAPIModuleWithValidationForSource(source, APIModule{API: api,
				Stream: func(_ Model, _ Context, o ProviderStreamOptions) *AssistantMessageEventStream {
					return emit(o.HTTPClient)
				},
				StreamSimple: func(_ Model, _ Context, o SimpleStreamOptions) *AssistantMessageEventStream {
					return emit(o.HTTPClient)
				},
			}, APIOptionValidation{
				ValidateOptions:       func(_ Model, o ProviderStreamOptions) error { return validate(o.HTTPClient) },
				ValidateSimpleOptions: func(_ Model, o SimpleStreamOptions) error { return validate(o.HTTPClient) },
			})
			func() {
				defer UnregisterAPIModules(source)
				var stream *AssistantMessageEventStream
				if simple {
					stream = StreamSimple(Model{API: api}, Context{}, SimpleStreamOptions{HTTPClient: client})
				} else {
					stream = Stream(Model{API: api}, Context{}, ProviderStreamOptions{HTTPClient: client})
				}
				defer stream.Close()
				for e := range stream.Events() {
					if strings.Contains(e.Error.ErrorMessage, "PRIVATE_OPTION_SECRET") {
						t.Fatal("preflight error leaked")
					}
				}
				result := stream.Result()
				want := 1
				if reject {
					want = 0
				}
				if validations != 1 || dispatches != want {
					t.Fatal("wrong dispatch count")
				}
				if reject && (result.StopReason != StopReasonError || result.ErrorMessage != "api options rejected") {
					t.Fatal("rejection terminal")
				}
			}()
		}
	}
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func TestAPIOptionPreflightAbsentPreservesLegacyRouting(t *testing.T) {
	for _, simple := range []bool{false, true} {
		source := t.Name() + string(rune('a'+boolInt(simple)))
		api := API(source)
		calls := 0
		emit := func(client *http.Client) *AssistantMessageEventStream {
			calls++
			if client == nil {
				t.Error("legacy default client missing")
			}
			return streamAPIUnavailable(Model{API: api}, "legacy fixture")
		}
		RegisterAPIModuleForSource(source, APIModule{API: api,
			Stream: func(_ Model, _ Context, o ProviderStreamOptions) *AssistantMessageEventStream {
				return emit(o.HTTPClient)
			},
			StreamSimple: func(_ Model, _ Context, o SimpleStreamOptions) *AssistantMessageEventStream {
				return emit(o.HTTPClient)
			},
		})
		func() {
			defer UnregisterAPIModules(source)
			var s *AssistantMessageEventStream
			if simple {
				s = StreamSimple(Model{API: api}, Context{}, SimpleStreamOptions{})
			} else {
				s = Stream(Model{API: api}, Context{}, ProviderStreamOptions{})
			}
			defer s.Close()
			for range s.Events() {
			}
			if got := s.Result(); calls != 1 || got.ErrorMessage != "legacy fixture" {
				t.Fatal("legacy routing changed")
			}
		}()
	}
}

func TestKiroHostOptionsAppliedBeforeHostDefaultClient(t *testing.T) {
	for _, simple := range []bool{false, true} {
		source := t.Name() + string(rune('a'+boolInt(simple)))
		api := API(source)
		calls := 0
		emit := func(client *http.Client) *AssistantMessageEventStream {
			calls++
			if client == nil {
				t.Error("usual host wrapping missing")
			}
			return streamAPIUnavailable(Model{API: api}, "injected executor fixture")
		}
		RegisterAPIModuleWithValidationForSource(source, APIModule{API: api,
			Stream: func(_ Model, _ Context, o ProviderStreamOptions) *AssistantMessageEventStream {
				return emit(o.HTTPClient)
			},
			StreamSimple: func(_ Model, _ Context, o SimpleStreamOptions) *AssistantMessageEventStream {
				return emit(o.HTTPClient)
			},
		}, APIOptionValidation{ValidateOptions: validateKiroHostOptions, ValidateSimpleOptions: validateKiroHostSimpleOptions})
		func() {
			defer UnregisterAPIModules(source)
			for _, custom := range []bool{false, true} {
				var client *http.Client
				if custom {
					client = &http.Client{}
				}
				var s *AssistantMessageEventStream
				if simple {
					s = StreamSimple(Model{API: api}, Context{}, SimpleStreamOptions{RequestContext: context.Background(), Transport: TransportSSE, HTTPClient: client})
				} else {
					s = Stream(Model{API: api}, Context{}, ProviderStreamOptions{RequestContext: context.Background(), Transport: TransportSSE, HTTPClient: client})
				}
				for range s.Events() {
				}
				result := s.Result()
				s.Close()
				want := "injected executor fixture"
				if custom {
					want = "api options rejected"
				}
				if result.ErrorMessage != want {
					t.Fatal("default vs supplied client not distinguished")
				}
			}
			if calls != 1 {
				t.Fatal("custom client reached callback")
			}
		}()
	}
}

func TestAPIOptionPreflightKeepsPublicModuleLayout(t *testing.T) {
	// This legacy positional literal must keep compiling.
	module := APIModule{API("legacy-positional"), nil, nil, ModelCapabilities{}}
	if module.API != "legacy-positional" {
		t.Fatal("public layout changed")
	}
}

func TestAPIOptionPreflightGenerationSnapshot(t *testing.T) {
	for _, simple := range []bool{false, true} {
		source := t.Name() + string(rune('a'+boolInt(simple)))
		api := API(source)
		entered, release := make(chan struct{}), make(chan struct{})
		oldCalls, newCalls := 0, 0
		validateOld := func() error { close(entered); <-release; return nil }
		old := APIModule{API: api,
			Stream: func(Model, Context, ProviderStreamOptions) *AssistantMessageEventStream {
				oldCalls++
				return streamAPIUnavailable(Model{API: api}, "old-generation")
			},
			StreamSimple: func(Model, Context, SimpleStreamOptions) *AssistantMessageEventStream {
				oldCalls++
				return streamAPIUnavailable(Model{API: api}, "old-generation")
			},
		}
		RegisterAPIModuleWithValidationForSource(source, old, APIOptionValidation{
			ValidateOptions:       func(Model, ProviderStreamOptions) error { return validateOld() },
			ValidateSimpleOptions: func(Model, SimpleStreamOptions) error { return validateOld() },
		})
		started := make(chan *AssistantMessageEventStream, 1)
		go func() {
			if simple {
				started <- StreamSimple(Model{API: api}, Context{}, SimpleStreamOptions{})
			} else {
				started <- Stream(Model{API: api}, Context{}, ProviderStreamOptions{})
			}
		}()
		awaitStreamSignal(t, entered)
		UnregisterAPIModules(source)
		replacement := source + "replacement"
		RegisterAPIModuleWithValidationForSource(replacement, APIModule{API: api,
			Stream: func(Model, Context, ProviderStreamOptions) *AssistantMessageEventStream {
				newCalls++
				return streamAPIUnavailable(Model{API: api}, "new-generation")
			},
			StreamSimple: func(Model, Context, SimpleStreamOptions) *AssistantMessageEventStream {
				newCalls++
				return streamAPIUnavailable(Model{API: api}, "new-generation")
			},
		}, APIOptionValidation{
			ValidateOptions:       func(Model, ProviderStreamOptions) error { return errors.New("deny new") },
			ValidateSimpleOptions: func(Model, SimpleStreamOptions) error { return errors.New("deny new") },
		})
		close(release)
		s := <-started
		for range s.Events() {
		}
		result := s.Result()
		s.Close()
		if result.ErrorMessage != "old-generation" || oldCalls != 1 || newCalls != 0 {
			t.Fatal("mixed registry generations")
		}
		if simple {
			s = StreamSimple(Model{API: api}, Context{}, SimpleStreamOptions{})
		} else {
			s = Stream(Model{API: api}, Context{}, ProviderStreamOptions{})
		}
		for range s.Events() {
		}
		result = s.Result()
		s.Close()
		UnregisterAPIModules(replacement)
		if result.ErrorMessage != "api options rejected" || newCalls != 0 {
			t.Fatal("replacement validation missing")
		}
	}
}

func TestAPIOptionPreflightConcurrentRegistryChurn(t *testing.T) {
	source := t.Name()
	api := API(source)
	var dispatches atomic.Int32
	module := APIModule{API: api, StreamSimple: func(Model, Context, SimpleStreamOptions) *AssistantMessageEventStream {
		dispatches.Add(1)
		return streamAPIUnavailable(Model{API: api}, "unexpected dispatch")
	}}
	validation := APIOptionValidation{ValidateSimpleOptions: func(Model, SimpleStreamOptions) error { return errors.New("deny") }}
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := 0; i < 100; i++ {
			RegisterAPIModuleWithValidationForSource(source, module, validation)
			UnregisterAPIModules(source)
		}
	})
	wg.Go(func() {
		for i := 0; i < 100; i++ {
			s := StreamSimple(Model{API: api}, Context{}, SimpleStreamOptions{})
			for range s.Events() {
			}
			s.Result()
			s.Close()
		}
	})
	wg.Wait()
	UnregisterAPIModules(source)
	if dispatches.Load() != 0 {
		t.Fatal("guard lost during registry churn")
	}
}
