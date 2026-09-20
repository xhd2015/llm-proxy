const object = value => value !== null && typeof value === 'object' && !Array.isArray(value);

export function modelTitle(model, fallback = 'Unnamed model') {
  return object(model) ? model.displayName || model.clientModelName || model.providerModelName || fallback : 'Invalid entry';
}

// Groups retain source indexes; rendering never reorders or rewrites config arrays.
export function modelGroups(doc) {
  const groups = [], byName = new Map();
  for (const [index, provider] of (Array.isArray(doc?.providers) ? doc.providers : []).entries()) {
    const name = object(provider) && typeof provider.name === 'string' ? provider.name : '';
    const group = {key:`provider:${index}`, name, label:name || `Unnamed provider ${index + 1}`, provider:index, models:[]};
    groups.push(group);
    if (name && !byName.has(name)) byName.set(name, group);
  }
  for (const [index, model] of (Array.isArray(doc?.models) ? doc.models : []).entries()) {
    const name = object(model) && typeof model.provider === 'string' ? model.provider : '';
    let group = byName.get(name);
    if (!group) {
      group = {key:`unknown:${name}`, name, label:name ? `Unknown provider: ${name}` : 'Missing provider', provider:-1, models:[]};
      groups.push(group); byName.set(name,group);
    }
    group.models.push({index, model});
  }
  return groups;
}

function matches(value, query) {
  return object(value) && ['name','displayName','clientModelName','providerModelName'].some(key => String(value[key] || '').toLowerCase().includes(query));
}

// A matching ancestor includes its descendants; a matching leaf retains its ancestors.
export function filterModelGroups(groups, search) {
  const query = search.trim().toLowerCase();
  return groups.flatMap(group => {
    const all = !query || group.label.toLowerCase().includes(query);
    const models = group.models.flatMap(entry => {
      const baseMatch = all || matches(entry.model,query);
      const variants = (Array.isArray(entry.model?.variants) ? entry.model.variants : [])
        .map((variant,index) => ({variant,index})).filter(({variant}) => baseMatch || matches(variant,query));
      return baseMatch || variants.length ? [{...entry,variants}] : [];
    });
    return all || models.length ? [{...group,models}] : [];
  });
}
