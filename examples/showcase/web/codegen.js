// A sov explorer extension (real ES module). The default export receives the
// sovx SDK and registers against it: Python + C# codegen, plus a bearer-token
// setting and a request hook that attaches it to every "try it" call. Nothing
// here is sov-specific plumbing — it is app-author code.
//
// Each codegen is registered with sovx.codegen(); the dashboard renders its output
// INLINE and colorized (never a button) — a "Request <label>" + "Response <label>"
// block on every method, and a "<label>" block on every type page.
export default function (sovx) {
  const PY = { string: 'str', number: 'float', integer: 'int', boolean: 'bool', object: 'dict' };
  const CS = { string: 'string', number: 'double', integer: 'int', boolean: 'bool', object: 'object' };

  const pascal = s => String(s || '').replace(/(^|[_\-.])(\w)/g, (_, __, c) => c.toUpperCase());

  // Array fields carry their element in typeName (named) or elemType (scalar);
  // resolve the element, then wrap per language (list[..] / List<..>).
  const pyType = f => f.schemaType === 'array'
    ? 'list[' + (f.typeName ? sovx.ident(f.typeName) : (PY[f.elemType] || 'object')) + ']'
    : (f.typeName ? sovx.ident(f.typeName) : (PY[f.schemaType] || 'object'));
  const csType = f => f.schemaType === 'array'
    ? 'List<' + (f.typeName ? sovx.ident(f.typeName) : (CS[f.elemType] || 'object')) + '>'
    : (f.typeName ? sovx.ident(f.typeName) : (CS[f.schemaType] || 'object'));

  // Teach the dashboard how to color the two languages this extension emits. An
  // app generating Go or Rust would register those instead.
  sovx.highlighter('python', [
    { cls: 'comment', re: /#.*/ },
    { cls: 'str', re: /'(?:[^'\\]|\\.)*'|"(?:[^"\\]|\\.)*"/ },
    { cls: 'kw', re: /@\w+|\b(?:class|def|return|pass|import|from|None|True|False)\b/ },
    { cls: 'type', re: /\b(?:str|int|float|bool|list|dict|object)\b/ },
    { cls: 'num', re: /\b\d+(?:\.\d+)?\b/ },
  ]);
  sovx.highlighter('csharp', [
    { cls: 'comment', re: /\/\/.*/ },
    { cls: 'str', re: /"(?:[^"\\]|\\.)*"/ },
    { cls: 'kw', re: /\b(?:public|sealed|class|get|set|static|void|new|namespace)\b/ },
    { cls: 'type', re: /\b(?:string|int|double|bool|object)\b/ },
    { cls: 'num', re: /\b\d+(?:\.\d+)?\b/ },
  ]);

  // Type names route through sovx.ident so generics/packages ("Page[main.Charge]")
  // emit a legal identifier; field names keep pascal (they are wire jsonNames).
  function pyClass(name, fields) {
    let out = '@dataclass\nclass ' + sovx.ident(name) + ':\n';
    if (!fields.length) return out + '    pass\n';
    for (const f of fields) out += '    ' + f.jsonName + ': ' + pyType(f) + '\n';
    return out;
  }
  function csClass(name, fields) {
    let out = 'public sealed class ' + sovx.ident(name) + '\n{\n';
    for (const f of fields) out += '    public ' + csType(f) + ' ' + pascal(f.jsonName) + ' { get; set; }\n';
    return out + '}\n';
  }

  // walk emits the shape's class plus every nested type it references (deduped),
  // so a Response block carries the full body, not just the outermost class.
  const walk = emit => shape => {
    const seen = new Set(), chunks = [];
    const visit = (name, fields) => {
      if (seen.has(name)) return;
      seen.add(name);
      chunks.push(emit(name, fields));
      for (const f of fields || []) {
        if (f.typeName && shape.nested && shape.nested[f.typeName]) visit(f.typeName, shape.nested[f.typeName]);
      }
    };
    visit(shape.name, shape.fields || []);
    return chunks.join('\n');
  };

  sovx.codegen({ id: 'python', label: 'Python', lang: 'python', render: walk(pyClass) });
  sovx.codegen({ id: 'csharp', label: 'C#', lang: 'csharp', render: walk(csClass) });

  // User-driven auth: a token the operator pastes once, attached to every request.
  sovx.setting({ id: 'bearer', label: 'Bearer token', placeholder: 'paste a token', secret: true });
  sovx.requestHook(req => {
    const t = sovx.setting('bearer');
    if (t) req.headers['Authorization'] = 'Bearer ' + t;
  });
}
