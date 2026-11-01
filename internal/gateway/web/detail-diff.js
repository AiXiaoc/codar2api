'use strict';

// Compare parsed JSON values while keeping spans in the formatted, lossless
// source. Object key order and JSON string escaping alone are not changes.
const DetailDiff = (() => {
  const MAX_NODES = 100000, MAX_DEPTH = 256, MAX_TOKENS = 20000, MAX_EDITS = 256, MAX_WORK = 5000000;
  const limitError = () => Object.assign(new Error('comparison limit'), {detailLimit: true});
  function tree(text){
    let i = 0, count = 0;
    const skip = () => { while(i < text.length && /\s/.test(text[i])) i++; };
    const str = () => {
      const start = i++;
      while(i < text.length){
        if(text[i] === '\\'){ i += 2; continue; }
        if(text[i++] === '"') return {type: 'string', start, end: i, value: JSON.parse(text.slice(start, i))};
      }
      throw new SyntaxError('unfinished string');
    };
    function value(depth){
      skip();
      if(depth > MAX_DEPTH || ++count > MAX_NODES) throw limitError();
      const start = i, ch = text[i];
      if(ch === '"') return str();
      if(ch === '{'){
        i++; skip(); const entries = [];
        if(text[i] !== '}') while(true){
          skip(); if(text[i] !== '"') throw new SyntaxError('missing object key');
          const key = str(); skip(); if(text[i++] !== ':') throw new SyntaxError('missing colon');
          const child = value(depth + 1); entries.push({key: key.value, keyStart: key.start, value: child});
          skip(); if(text[i] === '}') break;
          if(text[i++] !== ',') throw new SyntaxError('missing comma');
        }
        if(text[i++] !== '}') throw new SyntaxError('missing object end');
        return {type: 'object', start, end: i, entries};
      }
      if(ch === '['){
        i++; skip(); const items = [];
        if(text[i] !== ']') while(true){
          items.push(value(depth + 1)); skip(); if(text[i] === ']') break;
          if(text[i++] !== ',') throw new SyntaxError('missing comma');
        }
        if(text[i++] !== ']') throw new SyntaxError('missing array end');
        return {type: 'array', start, end: i, items};
      }
      while(i < text.length && !/[\s,\]}]/.test(text[i])) i++;
      const token = text.slice(start, i);
      if(!/^(?:true|false|null|-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?)$/.test(token)) throw new SyntaxError('invalid value');
      return {type: 'literal', start, end: i, value: token};
    }
    const root = value(0); skip(); if(i !== text.length) throw new SyntaxError('trailing data');
    return root;
  }

  function tokens(text){
    const out = [], re = /\s+|[\p{L}\p{N}_]+|[^\s\p{L}\p{N}_]/gu;
    let m;
    while((m = re.exec(text)) !== null){
      if(out.length >= MAX_TOKENS) throw limitError();
      out.push({value: m[0], start: m.index, end: re.lastIndex});
    }
    return out;
  }

  // Work and memory are bounded for large diagnostic bodies.
  function sequenceDiff(a, b, equal, weight, maxCells = 1000000){
    const n = a.length, m = b.length;
    // Prefer preserving meaningful words over matching incidental whitespace.
    // This also keeps an unchanged word unmarked when surrounding text moves.
    if(n * m <= maxCells){
      const width = m + 1, score = new Uint32Array((n + 1) * width);
      for(let i = n - 1; i >= 0; i--) for(let j = m - 1; j >= 0; j--){
        const same = equal(a[i], b[j]) ? weight(a[i]) + score[(i + 1) * width + j + 1] : 0;
        score[i * width + j] = Math.max(same, score[(i + 1) * width + j], score[i * width + j + 1]);
      }
      const ops = []; let i = 0, j = 0;
      while(i < n || j < m){
        const same = i < n && j < m && equal(a[i], b[j]) ? weight(a[i]) + score[(i + 1) * width + j + 1] : -1;
        if(same >= 0 && same === score[i * width + j]) ops.push({kind: 'equal', a: a[i++], b: b[j++]});
        else if(i < n && (j >= m || score[(i + 1) * width + j] >= score[i * width + j + 1])) ops.push({kind: 'delete', a: a[i++]});
        else ops.push({kind: 'insert', b: b[j++]});
      }
      return ops;
    }
    const trace = [], v = new Map([[1, 0]]); let work = 0, finalD = -1;
    const at = (map, key) => map.has(key) ? map.get(key) : -1;
    for(let d = 0; d <= Math.min(n + m, MAX_EDITS); d++){
      trace.push(new Map(v));
      for(let k = -d; k <= d; k += 2){
        if(++work > MAX_WORK) throw limitError();
        let x = (k === -d || (k !== d && at(v, k - 1) < at(v, k + 1))) ? at(v, k + 1) : at(v, k - 1) + 1;
        if(x < 0) x = 0;
        let y = x - k;
        while(x < n && y < m && equal(a[x], b[y])){
          if(++work > MAX_WORK) throw limitError();
          x++; y++;
        }
        v.set(k, x);
        if(x >= n && y >= m){ finalD = d; break; }
      }
      if(finalD >= 0) break;
    }
    if(finalD < 0) throw limitError();
    let x = n, y = m; const operations = [];
    for(let d = finalD; d >= 0; d--){
      const previous = trace[d], k = x - y;
      const prevK = (k === -d || (k !== d && at(previous, k - 1) < at(previous, k + 1))) ? k + 1 : k - 1;
      const prevX = Math.max(0, at(previous, prevK)), prevY = prevX - prevK;
      while(x > prevX && y > prevY){ operations.push({kind: 'equal', a: a[--x], b: b[--y]}); }
      if(d > 0){
        if(x === prevX) operations.push({kind: 'insert', b: b[--y]});
        else operations.push({kind: 'delete', a: a[--x]});
      }
    }
    return operations.reverse();
  }

  function tokenDiff(before, after){
    return sequenceDiff(tokens(before), tokens(after), (a, b) => a.value === b.value,
      token => /[\p{L}\p{N}_]/u.test(token.value) ? Math.min(token.value.length, 1000) * 2 + 3 : 1);
  }

  function splitsPair(text, i){
    if(i <= 0 || i >= text.length) return false;
    const a = text.charCodeAt(i - 1), b = text.charCodeAt(i);
    return a >= 0xd800 && a <= 0xdbff && b >= 0xdc00 && b <= 0xdfff;
  }
  function common(before, after){
    let start = 0, endA = before.length, endB = after.length;
    while(start < endA && start < endB && before[start] === after[start]) start++;
    if(splitsPair(before, start) || splitsPair(after, start)) start--;
    while(endA > start && endB > start && before[endA - 1] === after[endB - 1]){ endA--; endB--; }
    if(splitsPair(before, endA) || splitsPair(after, endB)){ endA++; endB++; }
    return {start, endA, endB};
  }
  function textChanges(before, after){
    if(before === after) return {ranges: [], removed: false};
    const outer = common(before, after);
    const a = before.slice(outer.start, outer.endA), b = after.slice(outer.start, outer.endB);
    if(!a) return {ranges: b ? [[outer.start, outer.endB]] : [], removed: false};
    if(!b) return {ranges: [], removed: true};
    const ops = tokenDiff(a, b), ranges = []; let removed = false;
    for(let i = 0; i < ops.length;){
      if(ops[i].kind === 'equal'){ i++; continue; }
      let aa = '', bb = '', bStart = null;
      while(i < ops.length && ops[i].kind !== 'equal'){
        const op = ops[i++];
        if(op.kind === 'delete') aa += op.a.value;
        else { if(bStart === null) bStart = op.b.start; bb += op.b.value; }
      }
      if(!bb){ if(aa) removed = true; continue; }
      const inner = common(aa, bb), start = outer.start + bStart + inner.start, end = outer.start + bStart + inner.endB;
      if(end > start) ranges.push([start, end]);
      if(inner.endA > inner.start && inner.endB === inner.start) removed = true;
    }
    return {ranges, removed};
  }

  // Map decoded JS string offsets back to the exact JSON string token. This
  // keeps escapes and very large numeric literals unchanged in the display.
  function encodedRanges(text, node, ranges){
    const points = [...new Set(ranges.flat())].sort((a, b) => a - b), mapped = new Map();
    let offset = 0, pos = node.start + 1;
    for(const point of points){
      while(offset < point){
        if(text[pos] === '\\') pos += text[pos + 1] === 'u' ? 6 : 2;
        else pos++;
        offset++;
      }
      mapped.set(point, pos);
    }
    return ranges.map(([a, b]) => [mapped.get(a), mapped.get(b)]);
  }
  const childPath = (path, key) => /^[A-Za-z_$][A-Za-z0-9_$]*$/.test(key) ? path + '.' + key : path + '[' + JSON.stringify(key) + ']';

  // Fingerprints only narrow comparisons; equalNode verifies matches, so
  // hash collisions cannot hide a modification.
  function fingerprint(node){
    if(node.signature) return node.signature;
    let h1 = 2166136261, h2 = 2246822519;
    const add = value => {
      value = String(value);
      for(let i = 0; i < value.length; i++){
        h1 = Math.imul(h1 ^ value.charCodeAt(i), 16777619);
        h2 = Math.imul(h2 ^ value.charCodeAt(i), 3266489917);
      }
      h1 = Math.imul(h1 ^ value.length, 16777619);
      h2 = Math.imul(h2 ^ value.length, 3266489917);
    };
    add(node.type);
    if(node.type === 'string' || node.type === 'literal') add(node.value);
    else if(node.type === 'array') for(const item of node.items) add(fingerprint(item));
    else for(const e of node.entries.slice().sort((a, b) => a.key < b.key ? -1 : a.key > b.key ? 1 : 0)){ add(e.key); add(fingerprint(e.value)); }
    return node.signature = (h1 >>> 0).toString(16) + ':' + (h2 >>> 0).toString(16);
  }
  function equalNode(a, b){
    if(a.type !== b.type || fingerprint(a) !== fingerprint(b)) return false;
    if(a.type === 'string' || a.type === 'literal') return a.value === b.value;
    if(a.type === 'array') return a.items.length === b.items.length && a.items.every((item, i) => equalNode(item, b.items[i]));
    if(a.entries.length !== b.entries.length) return false;
    const byKey = new Map();
    for(const entry of a.entries){ const list = byKey.get(entry.key) || []; list.push(entry); byKey.set(entry.key, list); }
    for(let i = b.entries.length - 1; i >= 0; i--){
      const entry = b.entries[i], list = byKey.get(entry.key), prior = list && list.pop();
      if(!prior || !equalNode(prior.value, entry.value)) return false;
    }
    return true;
  }

  function compare(before, after, options = {}){
    const result = {ranges: [], removed: [], changed: false, limited: false, reason: ''};
    if(before == null || after == null){ result.reason = '缺少原始或转发请求体，无法比较。'; return result; }
    if(options.beforeBinary || options.afterBinary){
      if(options.beforeBinary && options.afterBinary && before === after) return result;
      result.changed = true; result.reason = '二进制正文无法按文本高亮，请复制 Base64 内容比较。'; return result;
    }
    before = String(before); after = String(after);
    let oldTree, newTree;
    try { oldTree = tree(before); newTree = tree(after); }
    catch(err){
      if(err.detailLimit){ result.limited = true; result.reason = '正文嵌套或字段过多，无法精确高亮。'; return result; }
      try {
        const d = textChanges(before, after); result.ranges = d.ranges; result.changed = before !== after;
        if(d.removed) result.removed.push('原始请求体中的文本');
      } catch(e){ result.changed = before !== after; result.limited = true; result.reason = '正文差异过大，无法精确高亮。'; }
      return result;
    }
    const removed = new Set();
    function change(a, b, path){
      if(!a){ result.changed = true; result.ranges.push([b.start, b.end]); return; }
      if(!b){ result.changed = true; removed.add(path); return; }
      if(a.type !== b.type){ result.changed = true; result.ranges.push([b.start, b.end]); return; }
      if(a.type === 'string'){
        if(a.value === b.value) return;
        result.changed = true;
        try {
          const d = textChanges(a.value, b.value);
          result.ranges.push(...encodedRanges(after, b, d.ranges));
          if(d.removed) removed.add(path + ' 中的文本');
        } catch(err){ result.limited = true; }
        return;
      }
      if(a.type === 'literal'){
        if(a.value !== b.value){ result.changed = true; result.ranges.push([b.start, b.end]); }
        return;
      }
      if(a.type === 'array'){
        if(a.items.length === b.items.length){
          for(let i = 0; i < a.items.length; i++) change(a.items[i], b.items[i], path + '[' + i + ']');
          return;
        }
        result.changed = true;
        try {
          if(a.items.length > MAX_TOKENS || b.items.length > MAX_TOKENS) throw limitError();
          const oldItems = a.items.map((node, index) => ({node, index})), newItems = b.items.map((node, index) => ({node, index}));
          const ops = sequenceDiff(oldItems, newItems, (x, y) => equalNode(x.node, y.node), () => 1, 10000);
          for(let i = 0; i < ops.length;){
            if(ops[i].kind === 'equal'){ i++; continue; }
            const removedItems = [], addedItems = [];
            while(i < ops.length && ops[i].kind !== 'equal'){
              const op = ops[i++];
              if(op.kind === 'delete') removedItems.push(op.a);
              else addedItems.push(op.b);
            }
            const paired = Math.min(removedItems.length, addedItems.length);
            for(let j = 0; j < paired; j++) change(removedItems[j].node, addedItems[j].node, path + '[' + addedItems[j].index + ']');
            for(let j = paired; j < removedItems.length; j++) change(removedItems[j].node, null, path + '[' + removedItems[j].index + ']');
            for(let j = paired; j < addedItems.length; j++) change(null, addedItems[j].node, path + '[' + addedItems[j].index + ']');
          }
        } catch(err){ result.limited = true; }
        return;
      }
      const byKey = new Map();
      for(const entry of a.entries){
        const list = byKey.get(entry.key) || []; list.push(entry); byKey.set(entry.key, list);
      }
      // Last duplicate keys are paired first, matching JSON decoder behavior.
      for(let i = b.entries.length - 1; i >= 0; i--){
        const entry = b.entries[i], list = byKey.get(entry.key), prior = list && list.pop(), child = childPath(path, entry.key);
        if(prior) change(prior.value, entry.value, child);
        else { result.changed = true; result.ranges.push([entry.keyStart, entry.value.end]); }
      }
      for(const [key, entries] of byKey) for(const entry of entries){ result.changed = true; removed.add(childPath(path, key)); }
    }
    change(oldTree, newTree, '$');
    result.removed = [...removed];
    if(result.limited) result.reason = '部分正文差异过大，无法逐字高亮；其他可比较的修改仍已标出。';
    return result;
  }

  function render(text, ranges, escape){
    text = String(text);
    const sorted = ranges.filter(([a, b]) => Number.isInteger(a) && Number.isInteger(b) && a >= 0 && b > a && b <= text.length).sort((a, b) => a[0] - b[0]);
    const merged = [];
    for(const [a, b] of sorted){
      const last = merged[merged.length - 1];
      if(last && a <= last[1]) last[1] = Math.max(last[1], b);
      else merged.push([a, b]);
    }
    let html = '', pos = 0;
    for(const [a, b] of merged){ html += escape(text.slice(pos, a)) + '<mark class="detail-change">' + escape(text.slice(a, b)) + '</mark>'; pos = b; }
    return html + escape(text.slice(pos));
  }
  return {compare, render};
})();

if(typeof module !== 'undefined' && module.exports) module.exports = DetailDiff;
