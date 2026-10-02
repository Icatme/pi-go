(function(catalog, limits) {
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
  const hostFailures = new WeakMap();
  const weakGet = Function.call.bind(WeakMap.prototype.get), weakSet = Function.call.bind(WeakMap.prototype.set);
  const slice = Function.call.bind(String.prototype.slice);
  const lower = Function.call.bind(String.prototype.toLowerCase), replace = Function.call.bind(String.prototype.replace);
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
  function bytes(value) {
    let length=0;
    for(let i=0;i<value.length;i++) {
      const unit=value.charCodeAt(i);
      if(unit<128)length++;else if(unit<2048)length+=2;
      else if(unit>=0xd800 && unit<=0xdbff && i+1<value.length && value.charCodeAt(i+1)>=0xdc00 && value.charCodeAt(i+1)<=0xdfff){length+=4;i++;}
      else length+=3;
    }
    return length;
  }
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
    const tool = catalog[i], summary=create(null);
    summary.name=tool.name;summary.description=tool.description;
    if(tool.namespace!==undefined)summary.namespace=tool.namespace;
    summaries[i] = freeze(summary);
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
  function comparable(name) { return replace(lower(slice(name,0,128)), /[^a-z0-9]/g, ''); }
  function distance(a,b) {
    let previous=[]; for(let j=0;j<=b.length;j++)previous[j]=j;
    for(let i=1;i<=a.length;i++) {
      const next=[i];
      for(let j=1;j<=b.length;j++) {
        const remove=previous[j]+1, insert=next[j-1]+1, change=previous[j-1]+(a[i-1]===b[j-1]?0:1);
        next[j]=remove<insert?(remove<change?remove:change):(insert<change?insert:change);
      }
      previous=next;
    }
    return previous[b.length];
  }
  function unknownTool(name) {
    const wanted=comparable(name), matches=[];
    for(let i=0;i<summaries.length;i++) {
      const candidate=comparable(summaries[i].name);
      if(!wanted || !candidate)continue;
      const score=distance(wanted,candidate);
      if(score<=2 || candidate.indexOf(wanted)>=0 || wanted.indexOf(candidate)>=0) {
        let position=matches.length;
        while(position>0 && matches[position-1].score>score)position--;
        for(let j=matches.length;j>position;j--)matches[j]=matches[j-1];
        matches[position]={name:summaries[i].name,score};
        if(matches.length>3)matches.length=3;
      }
    }
    let message='Unknown or unavailable tools.'+slice(name,0,128)+'.';
    if(matches.length) {
      message+=' Did you mean ';
      for(let i=0;i<matches.length;i++){if(i)message+=', ';message+='tools.'+matches[i].name;}
      message+='?';
    }
    return failure('unknown_tool',message+' Use searchTools(query) or ALL_TOOLS; check availability with "name" in tools.');
  }
  const tools = new Proxy(available, {get(target,name) {
    if (typeof name !== 'string') return undefined;
    const d=descriptor(target,name); if(d) return d.value;
    if(name==='then' || name==='toJSON')return undefined;
    throw unknownTool(name);
  },has(target,name){return !!descriptor(target,name);}});
  const api = {
    tools, ALL_TOOLS:freeze(summaries),
    searchTools(query, options) { return new PromiseClass((resolve,reject)=>{try {resolve(request('search',encode({query, options:options===undefined?{}:options})));} catch(e){reject(e);}}); },
    describeTool(name) { return new PromiseClass((resolve,reject)=>{try {const v=request('describe',encode(name));resolve(v===null?undefined:deepFreeze(v));} catch(e){reject(e);}}); },
    describeNamespace(name) { return new PromiseClass((resolve,reject)=>{try {const v=request('namespace',encode(name));resolve(v===null?undefined:deepFreeze(v));} catch(e){reject(e);}}); },
    text(value) {request('text',typeof value==='string'?value:encode(value));},
    image(value) {request('image',encode(value));},
    store(key,value) {if(typeof key!=='string')throw failure('store_key','Store key must be a string');if(value===undefined){request('delete',key);return;}const json=encode(value);if(bytes(json)>limits.storeValueBytes)throw failure('store_limit','value exceeds store byte limit. store() is for small IDs or summaries; keep large data in local variables and show images with image().');request('store',key,json);},
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
      if(r.ok)p.resolve(r.value);else {const e=failure(r.error.code,r.error.message,r.error.callId,r.error.execution);e.reasonCode=r.error.reasonCode;weakSet(hostFailures,e,freeze({id,message:r.error.message,code:r.error.code,stack:e.stack}));p.reject(e);}
    },
    finish(value) {try {if(value!==undefined)api.text(value);return {ok:true};}catch(e){return {ok:false,error:e};}},
    error(value) {
      // Host errors are bounded before admission. Guest errors remain under heap
      // and context limits; the Go bridge caps copied strings independently.
      const original=weakGet(hostFailures,value);
      const message=original?original.message:string(value);
      const stack=original?original.stack:(value && typeof value==='object'?value.stack:undefined);
      const code=original?original.code:(value && typeof value.code==='string'?value.code:'script');
      // Only the exact rejection object created by settle can refer to a Go
      // cause. Public error properties, prototypes and proxies are not identity.
      const detail=create(null);
      detail.message=slice(message,0,1536);detail.stack=typeof stack==='string'?slice(stack,0,512):'';detail.code=slice(code,0,128);
      detail.causeId=original===undefined?null:original.id;
      return stringify(detail);
    }
  });
})
