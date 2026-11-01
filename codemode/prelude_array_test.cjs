// Auxiliary offline prelude checks under Node; NOT QuickJS/WASM integration.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const test = require('node:test');
const source = fs.readFileSync(process.env.PRELUDE_SOURCE || __dirname + '/prelude.js', 'utf8');

async function run(code) {
  const calls = [];
  const context = vm.createContext({
    __pigo_host: (...args) => {
      calls.push(args);
      return JSON.stringify({ok: true, value: null});
    }
  });
  const control = vm.runInContext(source, context)([{name: 'echo', description: 'fake only'}], {storeValueBytes: 4096});
  const promise = vm.runInContext('(async () => {' + code + '})()', context);
  for (const [op, id, name, args] of calls) {
    if (op === 'call') control.settle(Number(id), JSON.stringify({ok: true, value: JSON.parse(args)}));
  }
  return {value: await promise, calls};
}

for (const key of ['customer', '01', '-0', '-1', '1e0', '4294967295', '__proto__']) {
  test('reject enumerable non-index key ' + key + ' before tool dispatch', async () => {
    const result = await run('const a=[1];Object.defineProperty(a,' + JSON.stringify(key) +
      ',{value:"lost",enumerable:true});try{await tools.echo({values:a});return "accepted"}catch(e){return e.code}');
    assert.equal(result.value, 'unsupported_json');
    assert.deepEqual(result.calls, []);
  });
}

test('normal dense array reaches fake tool unchanged', async () => {
  const result = await run('return await tools.echo({values:[null,true,0.1,"中",[]]})');
  assert.equal(JSON.stringify(result.value), '{"values":[null,true,0.1,"中",[]]}');
  assert.equal(result.calls.length, 1);
});

test('named arrays are rejected by output and store before admission', async () => {
  for (const expression of ['text(a)', 'store("key",a)']) {
    const result = await run('const a=[1];a.extra=2;try{' + expression + ';return "accepted"}catch(e){return e.code}');
    assert.equal(result.value, 'unsupported_json');
    assert.deepEqual(result.calls, []);
  }
});

test('non-enumerable properties retain normal JSON omission behavior', async () => {
  const result = await run('const a=[1];Object.defineProperty(a,"extra",{value:2});text(a)');
  assert.deepEqual(result.calls, [['text', '[1]']]);
});

test('property accessors are not executed', async () => {
  const result = await run('let hits=0;const a=[1];Object.defineProperty(a,"extra",{enumerable:true,get(){hits++;return 2}});try{text(a)}catch(e){return e.code+":"+hits}');
  assert.equal(result.value, 'unsupported_json:0');
  assert.deepEqual(result.calls, []);
});

test('mutating global number/string helpers cannot admit a named key', async () => {
  const result = await run('Number.isInteger=()=>true;globalThis.String=()=>"customer";const a=[1];a.customer=2;try{text(a)}catch(e){return e.code}');
  assert.equal(result.value, 'unsupported_json');
  assert.deepEqual(result.calls, []);
});

test('sparse arrays still fail closed', async () => {
  const result = await run('try{text([,1])}catch(e){return e.code}');
  assert.equal(result.value, 'unsupported_json');
  assert.deepEqual(result.calls, []);
});

test('inherited descriptor value cannot disguise an accessor', async () => {
  for (const initial of ['[]', '{}']) {
    const result = await run('let hits=0;const a=' + initial + ';Object.defineProperty(a,"0",{enumerable:true,get(){hits++;return "real"}});Object.prototype.value="injected";try{text(a)}catch(e){return e.code+":"+hits}');
    assert.equal(result.value, 'unsupported_json:0');
    assert.deepEqual(result.calls, []);
  }
});
