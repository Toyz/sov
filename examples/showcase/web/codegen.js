// A sov explorer extension (real ES module). The default export receives the
// sovx SDK and registers against it: "Python" / "C#" codegen actions on any
// type, plus a bearer-token setting + a request hook that attaches it to every
// "try it" call. Nothing here is sov-specific plumbing — it is app-author code.
export default function (sovx) {
  const PY = { string: 'str', number: 'float', integer: 'int', boolean: 'bool', array: 'list', object: 'dict' };
  const CS = { string: 'string', number: 'double', integer: 'int', boolean: 'bool', array: 'object[]', object: 'object' };

  const pascal = s => String(s || '').replace(/(^|[_\-.])(\w)/g, (_, __, c) => c.toUpperCase());
  const fieldsOf = ctx => (ctx.descriptor && ctx.descriptor.fields) || [];

  function toPython(name, fields) {
    let out = '@dataclass\nclass ' + pascal(name) + ':\n';
    if (!fields.length) return out + '    pass\n';
    for (const f of fields) {
      const t = f.typeName ? pascal(f.typeName) : (PY[f.schemaType] || 'object');
      out += '    ' + f.jsonName + ': ' + t + '\n';
    }
    return out;
  }
  function toCSharp(name, fields) {
    let out = 'public sealed class ' + pascal(name) + '\n{\n';
    for (const f of fields) {
      const t = f.typeName ? pascal(f.typeName) : (CS[f.schemaType] || 'object');
      out += '    public ' + t + ' ' + pascal(f.jsonName) + ' { get; set; }\n';
    }
    return out + '}\n';
  }

  sovx.action('type', {
    id: 'gen-python', label: 'Python',
    run(ctx) {
      const code = toPython(ctx.name, fieldsOf(ctx));
      ctx.panel({ title: 'Python', body: '<pre>' + sovx.escapeHTML(code) + '</pre>', copyText: code });
    },
  });
  sovx.action('type', {
    id: 'gen-csharp', label: 'C#',
    run(ctx) {
      const code = toCSharp(ctx.name, fieldsOf(ctx));
      ctx.panel({ title: 'C#', body: '<pre>' + sovx.escapeHTML(code) + '</pre>', copyText: code });
    },
  });

  // User-driven auth: a token the operator pastes once, attached to every request.
  sovx.setting({ id: 'bearer', label: 'Bearer token', placeholder: 'paste a token', secret: true });
  sovx.requestHook(req => {
    const t = sovx.setting('bearer');
    if (t) req.headers['Authorization'] = 'Bearer ' + t;
  });
}
