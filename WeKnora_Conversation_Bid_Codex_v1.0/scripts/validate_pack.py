#!/usr/bin/env python3
"""文档包静态核验；不运行WeKnora，不代表产品业务测试通过。"""
from pathlib import Path
import json, re, hashlib, copy
from decimal import Decimal, ROUND_HALF_UP

ROOT=Path(__file__).resolve().parent.parent
checks=[]
def check(name,ok):
    if not ok: raise AssertionError(name)
    checks.append(name)
def load(path): return json.loads((ROOT/path).read_text(encoding='utf-8'))
def pointers(node,root):
    if isinstance(node,dict):
        if '$ref' in node:
            ref=node['$ref']
            check('local_ref:'+ref,ref.startswith('#/'))
            v=root
            for part in ref[2:].split('/'):
                key=part.replace('~1','/').replace('~0','~')
                check('resolve_ref:'+ref,isinstance(v,dict) and key in v)
                v=v[key]
        for value in node.values(): pointers(value,root)
    elif isinstance(node,list):
        for value in node: pointers(value,root)

def matches_type(value,t):
    return {'object':isinstance(value,dict),'array':isinstance(value,list),
            'string':isinstance(value,str),'null':value is None,
            'boolean':isinstance(value,bool),'integer':type(value) is int,
            'number':type(value) in (int,float)}.get(t,False)

def validate(value,schema,path='$'):
    """仅实现本包样例涉及的schema子集；非通用JSON Schema验证器。"""
    if 'type' in schema:
        ts=schema['type'] if isinstance(schema['type'],list) else [schema['type']]
        assert any(matches_type(value,t) for t in ts),(path,'type',ts)
    if 'const' in schema: assert value==schema['const'],(path,'const')
    if 'enum' in schema: assert value in schema['enum'],(path,'enum')
    for k in ['anyOf','oneOf']:
        if k in schema:
            successes=0
            for sub in schema[k]:
                try: validate(value,sub,path); successes+=1
                except AssertionError: pass
            assert (successes>=1 if k=='anyOf' else successes==1),(path,k,successes)
    if isinstance(value,dict):
        assert all(k in value for k in schema.get('required',[])),(path,'required')
        props=schema.get('properties',{})
        if schema.get('additionalProperties') is False: assert set(value)<=set(props),(path,'extra')
        for k,v in value.items():
            if k in props: validate(v,props[k],path+'.'+k)
    if isinstance(value,list):
        assert len(value)>=schema.get('minItems',0),(path,'minItems')
        if 'maxItems' in schema: assert len(value)<=schema['maxItems'],(path,'maxItems')
        if schema.get('uniqueItems'): assert len({json.dumps(v,sort_keys=True) for v in value})==len(value),(path,'uniqueItems')
        for i,v in enumerate(value): validate(v,schema.get('items',{}),f'{path}[{i}]')
    if isinstance(value,str):
        assert len(value)>=schema.get('minLength',0),(path,'minLength')
        if 'pattern' in schema: assert re.search(schema['pattern'],value),(path,'pattern')
    if type(value) in (int,float):
        if 'minimum' in schema: assert value>=schema['minimum'],(path,'minimum')
        if 'maximum' in schema: assert value<=schema['maximum'],(path,'maximum')
        if 'exclusiveMinimum' in schema: assert value>schema['exclusiveMinimum'],(path,'exclusiveMinimum')

files=[p for p in ROOT.rglob('*') if p.is_file()]
for p in files:
    if p.suffix=='.json': json.loads(p.read_text(encoding='utf-8'))
check('JSON parses',True)
for p in ROOT.rglob('*.md'):
    text=p.read_text(encoding='utf-8')
    check('fences:'+str(p.relative_to(ROOT)),sum(1 for line in text.splitlines() if line.lstrip().startswith('```'))%2==0)
    for target in re.findall(r'\]\(([^)]+)\)',text):
        if '://' in target or target.startswith('#'): continue
        target=target.split('#')[0]
        check('link:'+target,(p.parent/target).exists())
check('12 phase prompts',len(list((ROOT/'prompts').glob('P[0-9][0-9]_*.md')))==12)
api=load('contracts/openapi.json')
check('OpenAPI version',api['openapi']=='3.1.0')
pointers(api,api)
ids=[]
for path,methods in api['paths'].items():
    for method,op in methods.items():
        ids.append(op['operationId'])
        required=set(re.findall(r'\{([^}]+)\}',path))
        actual={p['name'] for p in op['parameters'] if p['in']=='path' and p.get('required')}
        check('pathparams:'+op['operationId'],required==actual)
        if method not in ['get','head']:
            check('idempotency:'+op['operationId'],any(p['name']=='Idempotency-Key' and p.get('required') for p in op['parameters']))
check('unique operationIds',len(ids)==len(set(ids)))
for stem in ['analysis','document','card']:
    validate(load(f'fixtures/{stem}.valid.json'),load(f'contracts/{stem}.schema.json'))
    check('fixture schema:'+stem,True)
events=load('fixtures/events.valid.json')
for e in events: validate(e,load('contracts/event.schema.json'))
check('event seq', [e['seq'] for e in events]==[1,2,3])

a=load('fixtures/analysis.valid.json')
blocks={b['id']:b for b in load('fixtures/source_blocks.json')}
lotids={l['id'] for l in a['lots']}
for item in a['lots']+a['requirements']:
    for source in item['sources']:
        block=blocks[source['locator']['block_id']]
        check('source:'+item['id'],source['quote'] in block['text'] and source['locator']['file_id']==block['file_id'])
for r in a['requirements']:
    check('scope:'+r['id'],set(r['lot_ids'])<=lotids and (r['scope_mode']!='explicit' or len(r['lot_ids'])>0))
expected=load('fixtures/selection.expected.json')
selected=set(expected['selected_lot_ids'])
included={r['id'] for r in a['requirements'] if r['scope_mode']=='all_lots' or (r['scope_mode']=='explicit' and selected.intersection(r['lot_ids']))}
check('lot2 expected scope', included==set(expected['included_requirement_ids']))
check('no unselected requirements', not included.intersection(expected['excluded_requirement_ids']))
check('unresolved stays out',not included.intersection(expected['unresolved_requirement_ids']))
check('coverage accounting',{u['range_label'] for u in a['coverage_units']}==set(blocks))
q=load('fixtures/quote.expected.json'); unit=Decimal('0.01')
nets=[(Decimal(x['qty'])*Decimal(x['unit_price'])).quantize(unit,rounding=ROUND_HALF_UP) for x in q['lines']]
check('line decimals',nets==[Decimal(x['net']) for x in q['lines']])
net=sum(nets,Decimal(0)); tax=(net*Decimal(q['tax_rate'])).quantize(unit,rounding=ROUND_HALF_UP)
check('quote totals',net==Decimal(q['net_total']) and tax==Decimal(q['tax_total']) and net+tax==Decimal(q['gross_total']))
for stem,mutator in [('analysis',lambda v:v.pop('analysis_id')),('document',lambda v:v.update({'content_revision':-1})),('card',lambda v:v.update({'status':'imagined_success'}))]:
    bad=copy.deepcopy(load(f'fixtures/{stem}.valid.json')); mutator(bad)
    rejected=False
    try: validate(bad,load(f'contracts/{stem}.schema.json'))
    except AssertionError: rejected=True
    check('schema rejects bad '+stem,rejected)
sql=(ROOT/'design/schema_reference.sql').read_text(encoding='utf-8')
tables=set(re.findall(r'CREATE TABLE (\w+)',sql))
check('SQL table reference names',set(re.findall(r'REFERENCES (\w+)',sql))<=tables)
check('SQL no destructive statements',not re.search(r'^\s*(DROP|TRUNCATE)\b',sql,re.M|re.I))
manifest=ROOT/'SHA256SUMS.txt'
if manifest.exists():
    for line in manifest.read_text().splitlines():
        digest,filename=line.split('  ',1)
        check('sha256:'+filename,hashlib.sha256((ROOT/filename).read_bytes()).hexdigest()==digest)
print(json.dumps({'status':'passed','checks':len(checks),'operations':len(ids),'sql_tables':len(tables),'scope':'文档包静态结构、契约引用、样例schema子集、来源与标段样例、十进制金额；未运行WeKnora/数据库/模型/导出'},ensure_ascii=False,indent=2))
