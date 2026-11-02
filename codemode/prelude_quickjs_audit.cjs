// Optional test-only adapter for the repository's pinned QuickJS WASM asset.
// No Go bridge, real tools, filesystem/WASI descriptors or module loader granted.
const fs = require('node:fs');
const crypto = require('node:crypto');
const bytes = fs.readFileSync(process.env.QUICKJS_ASSET || __dirname + '/internal/assets/quickjs.wasm');
const digest = crypto.createHash('sha256').update(bytes).digest('hex');
if (digest !== 'd4c9375f2b1ca4dc95f72c8aa2982a7a9951ac8011490d79c6582df732b4bbd9') throw Error('unexpected QuickJS asset');
const moduleObject = new WebAssembly.Module(bytes);

module.exports = function run(source, code) {
  let instance;
  const deadline = Date.now() + 5000;
  const unsupported = () => { throw Error('unexpected external import'); };
  const imports = {
    env: {
      host_get_timezone_offset: () => 0,
      host_interrupt: () => Date.now() > deadline ? 1 : 0,
      host_promise_rejection: unsupported,
      host_module_normalize: unsupported,
      host_module_load: unsupported,
      host_call: unsupported,
    },
    wasi_snapshot_preview1: {
      clock_time_get: (id, precision, out) => { new DataView(instance.exports.memory.buffer).setBigUint64(out, 0n, true); return 0; },
      random_get: (out, size) => { new Uint8Array(instance.exports.memory.buffer, out, size).fill(0x5a); return 0; },
      fd_close: () => 8, fd_fdstat_get: () => 8, fd_seek: () => 8, fd_write: () => 8,
    }
  };
  instance = new WebAssembly.Instance(moduleObject, imports);
  const q = instance.exports;
  q._initialize();
  if (q.qjs_init() !== 0) throw Error('QuickJS initialization failed');
  q.qjs_set_memory_limit(64 * 1024 * 1024);
  q.qjs_set_max_stack_size(512 * 1024);
  q.qjs_set_interrupt_handler(1);
  function alloc(text) {
    const b = Buffer.from(text);
    const p = q.wasm_malloc(b.length + 1);
    if (!p) throw Error('allocation failed');
    new Uint8Array(q.memory.buffer, p, b.length).set(b);
    new Uint8Array(q.memory.buffer)[p + b.length] = 0;
    return [p, b.length];
  }
  function read(value) {
    const p = q.qjs_get_string(value);
    if (!p) throw Error('string conversion failed');
    const memory = new Uint8Array(q.memory.buffer);
    let end = p;
    while (end < memory.length && memory[end] !== 0) end++;
    const result = Buffer.from(memory.subarray(p, end)).toString('utf8');
    q.qjs_free_cstring(p);
    return result;
  }
  function evaluate(text) {
    const [p, length] = alloc(text), [name] = alloc('offline-audit.js');
    let value;
    try { value = q.qjs_eval(p, length, name, 0); }
    finally { q.wasm_free(p); q.wasm_free(name); }
    if (!value) throw Error('missing evaluation result');
    if (q.qjs_is_exception(value)) {
      q.qjs_free_value(value);
      const error = q.qjs_get_exception();
      try { throw Error(read(error)); } finally { q.qjs_free_value(error); }
    }
    return value;
  }
  try {
    const program = `globalThis.__auditResult = undefined;
(() => {
  const calls = [], encode = JSON.stringify.bind(JSON), arrayPrototype = Array.prototype;
  globalThis.__pigo_host = (...args) => {calls.push(args);return encode({ok:true,value:null});};
  const control = (${source})([{name:"echo",description:"fake only"}],{storeValueBytes:4096});
  const promise = (async () => {${code}})();
  for (const [op,id,name,args] of calls) if (op === "call")
    control.settle(+id,encode({ok:true,value:JSON.parse(args)}));
  promise.then(value => {
    // Restore only the intentionally-mutated test intrinsic before serializing
    // the audit transcript. This is not a general hostile-guest host bridge.
    delete arrayPrototype.toJSON;
    globalThis.__auditResult = encode({value,calls});
  }, error => {delete arrayPrototype.toJSON;globalThis.__auditResult=encode({auditError:"guest rejection"});});
})();`;
    const initial = evaluate(program); q.qjs_free_value(initial);
    let jobs = 0;
    while (q.qjs_is_job_pending()) {
      if (++jobs > 10000 || Date.now() > deadline) throw Error('job budget exceeded');
      if (q.qjs_execute_pending_job() < 0) throw Error('QuickJS job failed');
    }
    const result = evaluate('globalThis.__auditResult');
    try {
      const output = JSON.parse(read(result));
      if (output.auditError) throw Error(output.auditError);
      return output;
    } finally { q.qjs_free_value(result); }
  } finally { q.qjs_destroy(); }
};
