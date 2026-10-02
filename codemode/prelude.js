(function(catalog) {
  'use strict';
  const host = globalThis.__pigo_host;
  delete globalThis.__pigo_host;
  const stringify = JSON.stringify.bind(JSON), parse = JSON.parse.bind(JSON);
  const ownKeys = Reflect.ownKeys.bind(Reflect), descriptor = Object.getOwnPropertyDescriptor.bind(Object);
  const create = Object.create.bind(Object), freeze = Object.freeze.bind(Object);
  const getPrototype = Object.getPrototypeOf.bind(Object), objectPrototype = Object.prototype;
  const isArray = Array.isArray.bind(Array), isFiniteNumber = Number.isFinite.bind(Number), isInteger = Number.isInteger.bind(Number);
  const isSafeInteger = Number.isSafeInteger.bind(Number), PromiseClass = Promise, ErrorClass = Error;
  const string = String, define = Object.defineProperty.bind(Object);
  const setPrototype = Object.setPrototypeOf.bind(Object);
  const pending = new Map();
  const mapGet = Function.call.bind(Map.prototype.get), mapSet = Function.call.bind(Map.prototype.set);
  const mapDelete = Function.call.bind(Map.prototype.delete);
  let sequence = 0;

  function failure(code, message, callId, execution) {
    const e = new ErrorClass(message);
    e.code = code; e.callId = callId;
    e.execution = execution || {local: 'not_started', remote: 'not_dispatched'};
    return e;
  }
  function safe(value) {
    let nodes = 0; const ancestors = [];
    function visit(v, depth) {
      if (++nodes > 65536 || depth > 128) throw failure('json_limit', 'JSON nesting or node limit exceeded');
      if (v === null || typeof v === 'string' || typeof v === 'boolean') return v;
      if (typeof v === 'number') {
        if (!isFiniteNumber(v) || (isInteger(v) && !isSafeInteger(v))) throw failure('unsafe_number', 'Number is nonfinite or outside the safe integer range');
        return v;
      }
      if (typeof v !== 'object') throw failure('unsupported_json', 'Value is not supported JSON');
      for (let i=0;i<ancestors.length;i++) if (ancestors[i] === v) throw failure('unsupported_json', 'Cyclic JSON value');
      const array = isArray(v), proto = getPrototype(v);
      if (!array && proto !== objectPrototype && proto !== null) throw failure('unsupported_json', 'Only plain JSON objects are supported');
      ancestors[ancestors.length] = v;
      const out = array ? [] : create(null);
      if(array)setPrototype(out,null);
      const keys = ownKeys(v);
      for (let i=0;i<keys.length;i++) {
        const k = keys[i];
        if (array && k === 'length') continue;
        if (typeof k !== 'string') throw failure('unsupported_json', 'Symbol keys are not supported');
        const d = descriptor(v, k);
        if (!d || !d.enumerable) continue;
        if (!('value' in d)) throw failure('unsupported_json', 'JSON accessors are not supported');
        define(out, k, {value:visit(d.value,depth+1), enumerable:true, configurable:true, writable:true});
      }
      if (array && out.length !== v.length) throw failure('unsupported_json', 'Sparse arrays are not supported');
      for (let i=0;array && i<v.length;i++) if (!descriptor(out,string(i))) throw failure('unsupported_json', 'Sparse arrays are not supported');
      ancestors.length--; return out;
    }
    return visit(value,0);
  }
  function encode(v) { return stringify(safe(v)); }
  function request(op, ...args) {
    const r = parse(host(op, ...args));
    if (!r.ok) {const e=failure(r.error.code, r.error.message, r.error.callId, r.error.execution);e.reasonCode=r.error.reasonCode;throw e;}
    return r.value;
  }
  function deepFreeze(v) {
    if (v && typeof v === 'object') {
      const keys = ownKeys(v); for (let i=0;i<keys.length;i++) deepFreeze(v[keys[i]]);
      freeze(v);
    }
    return v;
  }
  const summaries = [], available = create(null);
  for (let i=0;i<catalog.length;i++) {
    const tool = catalog[i]; summaries[i] = freeze({name:tool.name, description:tool.description});
    define(available,tool.name,{enumerable:true,value:function(args) {
      return new PromiseClass((resolve,reject)=>{
        let payload; try {payload=encode(args === undefined ? {} : args);} catch(e) {reject(e);return;}
        const id = ++sequence;
        mapSet(pending,id,{resolve,reject});
        try {request('call',string(id),tool.name,payload);} catch(e) {mapDelete(pending,id);reject(e);}
      });
    }});
  }
  freeze(available);
  const tools = new Proxy(available, {get(target,name) {
    if (typeof name !== 'string') return undefined;
    const d=descriptor(target,name); if(d) return d.value;
    return function(){return PromiseClass.reject(failure('unknown_tool','Unknown or unavailable tool: '+name));};
  },has(target,name){return !!descriptor(target,name);}});
  const api = {
    tools, ALL_TOOLS:freeze(summaries),
    searchTools(query, options) { return new PromiseClass((resolve,reject)=>{try {resolve(request('search',encode({query, options:options===undefined?{}:options})));} catch(e){reject(e);}}); },
    describeTool(name) { return new PromiseClass((resolve,reject)=>{try {const v=request('describe',encode(name));resolve(v===null?undefined:deepFreeze(v));} catch(e){reject(e);}}); },
    describeNamespace(name) { return new PromiseClass((resolve,reject)=>{try {const v=request('namespace',encode(name));resolve(v===null?undefined:deepFreeze(v));} catch(e){reject(e);}}); },
    text(value) {request('text',typeof value==='string'?value:encode(value));},
    image(value) {request('image',encode(value));},
    store(key,value) {if(typeof key!=='string')throw failure('store_key','Store key must be a string');if(value===undefined){request('delete',key);return;}request('store',key,encode(value));},
    load(key) {if(typeof key!=='string')throw failure('store_key','Store key must be a string');const v=request('load',key);return v===null?undefined:parse(v);},
    exit() {request('exit');throw failure('exit','Execution finished');}
  };
  const console = create(null);
  for (const name of ['log','info','warn','error','debug']) define(console,name,{value:function(...args){
    let result='';for(let i=0;i<args.length;i++){if(i)result+=' ';result+=typeof args[i]==='string'?args[i]:encode(args[i]);}
    request('text',result);
  }});
  api.console=freeze(console);
  for (const name of ownKeys(api)) define(globalThis,name,{value:api[name],writable:false,configurable:false});
  // The control object remains in a Go-owned handle, never on globalThis.
  return freeze({
    settle(id, response) {
      const p=mapGet(pending,id); if(!p)return; mapDelete(pending,id);
      const r=parse(response);
      if(r.ok)p.resolve(r.value);else {const e=failure(r.error.code,r.error.message,r.error.callId,r.error.execution);e.reasonCode=r.error.reasonCode;p.reject(e);}
    },
    finish(value) {if(value!==undefined)api.text(value);},
    error(value) {
      // Host errors are bounded before admission. Guest errors remain under heap
      // and context limits; the Go bridge caps copied strings independently.
      const message=value && typeof value==='object' ? string(value) + (value.stack ? '\n'+string(value.stack):'') : string(value);
      const code=value && typeof value.code==='string'?value.code:'script';
      const callId=value && (typeof value.callId==='string'||typeof value.callId==='number')?value.callId:null;
      return stringify({message:message.slice(0,8192),code:code.slice(0,128),callId});
    }
  });
})
