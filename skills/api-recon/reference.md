# api-recon reference

Grep recipes, config.json templates, and troubleshooting. Run grep against js/. For single-line bundles, optionally use js-beautify or `sed 's/}/}\n/g'`; raw grep with context windows is usually enough.

## Reference scripts

Every file in scripts/ is a reference template requiring target-specific adaptation before execution.

| Script | Common adaptations |
| --- | --- |
| harvest_static.py | Endpoint regex, webpack/Vite manifests, microfrontend publicPath, retries/concurrency |
| runtime_harvest.js | Neutralize field/success values, stub matches/body shape, route sources, WS capture, waitUntil/routeTimeout/proxy |
| preload.js | loginPathRe, L1 stubs, neutralize.fields, apiPattern, L3, recordDetail, observe.*, neutralizeVueRouter |
| spider_mpa.py | Destructive-link exclusions, cookies, depth/max, same-domain filtering |
| extract_route_map.py | routeMap/routeLink regex and KEY naming |
| build_perm_tree.py | userRouteAuth parsing, ROOTS/PREFIX_PARENT hierarchy, outer stub fields |
| config.json | Central target-specific configuration |

Save adapted copies in the task working directory (for example recon/) and document concrete differences from reference scripts in the report.

## A. Reverse engineer three gates

### A1. Rendering: what determines logged-in state?

```bash
grep -rhoaE '.{0,40}(isLogin|isAuthenticated|loggedIn|hasLogin|requireAuth)\b.{0,80}' js | head
grep -rhoaE 'function (getUser|getToken|getAuth)[0-9]?\([^)]*\)\{.{0,200}' js | head
grep -rhoaE '(localStorage|sessionStorage)\.getItem\("[^"]+"\)' js | sort -u
grep -rhoaE '(Cookies?|cookie)\.(get|load)\("[^"]+"\)' js | sort -u
grep -rhoaE '\batob\(|JSON\.parse\(|jwt|decode' js | head
```

Trace `isLogin = f(getUser())` → `getUser = decode(storage.read(KEY))` to identify storage key, cookie/localStorage container, and encoding.

| Encoding | Mock configuration |
| --- | --- |
| Plain string / "1" / token | `"value": "anything-truthy"` |
| JSON.parse(x) | `"value": "json:{\"id\":1,\"username\":\"admin\"}"` |
| JSON.parse(atob(x)) | `"value": "b64json:{\"id\":1,\"username\":\"admin\"}"` |
| JWT | Unsigned/alg:none JWT, or a signature using a key embedded in the bundle |
| SM2/AES/RSA | Locate embedded keys; forge a decodable blob only if that satisfies the rendering gate, otherwise use static fallback |

Record this in cookies/localStorage configuration.

### A2. Interceptor: what redirects to /login?

```bash
grep -rhoaE '.{0,60}(interceptors\.response|axios|request\.use).{0,120}' js | head
grep -rhoaE '.{0,40}(response_code|errcode|errno|\bcode\b|\bret\b|\bstatus\b)\s*[=!]==?\s*[\-0-9]{1,4}.{0,60}' js | head -20
grep -rhoaE '.{0,40}(not logged in|please log in|login expired|unauthorized|authentication failed|authorization|token.{0,10}invalid).{0,40}' js | head
grep -rhoaE '.{0,30}(location\.href|router\.(push|replace)|navigate)\([^)]*login[^)]*\)' js | head
```

Identify the field name, success value (usually 0/200), and failure values causing redirects. Validate with a junk session:

```bash
curl -sk -X POST -H 'Cookie: <fakekey>=junk' https://target/api/<protected> -d '{}' -H 'Content-Type: application/json'
```

Set neutralize.fields and neutralize.success.

### A3. Content: where do menus/permissions come from?

```bash
grep -rhoaE '"/api[^"]*(permission|perm|role|menu|acl|resource|nav)[^"]*"' js | sort -u
grep -rhoaE '.{0,30}(menus|permissions|menuList|routeList|authList|role_permissions)\b.{0,120}' js | head
grep -rhoaE 'userRouteAuth|getResultTree|routeMap|routeLink|hasPermission|checkAuth' js | head
grep -rhoaE '([A-Z_][A-Z0-9_]*):\{name:"[^"]*",link:"/[^"]+"\}' js | head
```

Common enterprise interfaces use two data layers:

| Source | Typical payload | Consumer |
| --- | --- | --- |
| role_permissions API | permissions array and role_type | Route guards/button ACL |
| permissions/all API | Tree of code/position/children | Sidebar rendering |
| Bundle userRouteAuth | CODE → url/name | Permission code → frontend path |
| Bundle routeMap | KEY → name/link | Alias resolution, such as webpack o.DASHBOARD |

Read consumers to establish how getResultTree filters tree/permissions and which fields v-if/hasAuth checks. Small sites may use permissive payloads in stubs. For larger sites whose sidebar/modules remain blank, restore the full tree using section I.

## B. config.json template

```json
{
  "baseUrl": "https://target/",
  "runtimeMode": "both",
  "chromium": "/usr/bin/chromium",

  "cookies": [
    { "name": "auth", "value": "b64json:{\"id\":1,\"username\":\"admin\",\"role\":\"admin\",\"func\":{},\"permissions\":[\"*\"]}" }
  ],
  "localStorage": { "token": "faketoken", "isLogin": "1" },

  "neutralize": {
    "fields": ["response_code", "code", "errno", "ret", "status"],
    "success": 0,
    "flags": { "success": true, "message": "ok" }
  },
  "forward": true,
  "loginUrlPattern": "/login",
  "apiPattern": "/api/|/rest/|/graphql",

  "mockTier": "L1+L2",
  "recordDetail": true,
  "observe": {
    "storageReads": false,
    "cookieReads": false,
    "xhrHeaders": true
  },
  "neutralizeVueRouter": true,
  "stubs": [
    {
      "match": "permissions/all|/menu|role_permissions",
      "body": {
        "response_code": 0, "code": 0,
        "data": {
          "permissions": ["*"],
          "menus": [
            { "name": "dashboard", "path": "/dashboard", "show": true, "children": [] },
            { "name": "alert", "path": "/alert", "show": true, "children": [] }
          ]
        }
      }
    }
  ],

  "explore": {
    "clickTabs": true,
    "clickTables": true,
    "pushStateFallback": true,
    "maxMenuItems": 50
  },

  "routes": ["/dashboard", "/alert", "/asset", "/device", "/report", "/config", "/system"],
  "waitMs": 1500, "perRouteMs": 900, "headless": true,
  "waitUntil": "domcontentloaded",
  "routeTimeout": 12000,
  "proxy": "",

  "captureResponses": true, "recordWs": true, "respMax": 600
}
```

- runtimeMode: depth (Puppeteer), coverage (browser MCP), or both.
- Cookie value prefixes: b64json means base64(JSON); json means raw JSON; no prefix means literal text.
- forward=true forwards requests and patches business codes; false uses fully offline stubs.
- mockTier selects coverage preload layers, such as L1+L2 or L1+L2+L3.
- Populate routes from routes.txt; after menus are mocked, the harness appends anchor elements.
- captureResponses/recordWs apply to depth only.
- Large SPAs should use domcontentloaded to avoid networkidle2 hanging.
- routeTimeout is per-route page.goto timeout in milliseconds.
- proxy sets Puppeteer's --proxy-server; HTTP_PROXY/HTTPS_PROXY are also supported.

### B1. Separate role_permissions and permissions/all stubs

```json
"stubs": [
  {
    "match": "role_permissions",
    "body": {
      "response_code": 0,
      "data": {
        "permissions": ["MONITOR", "MONITOR_ALERT", "THREAT", "ASSETS_RISK"],
        "role_type": "SUPER_ADMIN"
      }
    }
  },
  {
    "match": "permissions/all",
    "body": {
      "response_code": 0,
      "data": [
        {
          "code": "MONITOR",
          "position": 1,
          "children": [
            { "code": "MONITOR_ALERT", "position": 1, "children": [] }
          ]
        }
      ]
    }
  }
]
```

Outer response_code/code/data fields must match A2's interceptor. permissions must cover every leaf code in the tree.

## C. Coverage preload configuration

Edit the copied preload.js CONFIG object or replace it before CDP injection:

```javascript
const CONFIG = {
  loginPathRe: /\/(login|signin)(\/|$|\?)/i,
  mockTier: 'L1+L2',
  forward: true,
  recordDetail: true,
  extractUrlsFromResponse: true,
  neutralizeVueRouter: true,
  observe: { storageReads: false, cookieReads: false, xhrHeaders: true },
  neutralize: { fields: ['response_code', 'code'], success: 0 },
  stubs: [ /* Same as config.json stubs */ ],
  apiPattern: /\/(api|apis|v\d+|dev|internal|graphql)\//i,
};
```

Verify `window.__API_RECON_PRELOAD__ === true` and a stable pathname. Export recordings:

```javascript
JSON.stringify({
  apis: [...window.__API_RECON_LOG__],
  detail: window.__API_RECON_DETAIL__,
  routes: [...(window.__API_RECON_ROUTES__ || [])],
  observe: window.__API_RECON_OBSERVE__,
}, null, 2)
```

## D. Preload/runtime hook capabilities

| Hook | Discovery value | Coverage |
| --- | --- | --- |
| fetch / XHR.open | Request URL/method | recordDetail + API log |
| XHR.setRequestHeader | Authorization and other headers | observe.xhrHeaders |
| Storage/cookie reads | Session keys | Optional observe.storageReads/cookieReads |
| Vue routes | Complete frontendRoutes | Loaded routes in __API_RECON_ROUTES__ |
| Vue guards/login redirects | Mount modules and trigger APIs | neutralizeVueRouter and native redirect neutralization |
| React routes | More routes | Static extraction/clicks; no dedicated hook |
| Login-path navigation blocking | Stay on page | Blocks login paths only, preserving business navigation |
| CryptoJS/SM encryption hooks | Plaintext parameters | Manual encryption-argument hook required; record conclusions in config |
| Anti-debugging | Runtime visibility | Manual handling required; static discovery remains usable |

## E. Endpoint regex when static output is sparse

Widen extract_endpoints in the copied harvest_static.py or use:

```bash
grep -rhoaE '"/[a-z][A-Za-z0-9_/\-]{3,}"' js | sort -u
grep -rhoaE '/api/[a-zA-Z0-9_./-]+' js | sort -u
```

## F. Troubleshooting

| Symptom | Cause / action |
| --- | --- |
| Few static APIs | Endpoint dialect mismatch; widen regex (section E) |
| Fewer chunks than manifest | CSS-only/undeployed chunks; retry 404s |
| Runtime still shows login | Recheck A1 storage key/container/encoding/domain |
| Shell renders but modules blank | Content gate; mock menus and verify route paths |
| Only bootstrap/locale per route | Restore permissions in I; check both permission stubs |
| Sidebar present, child pages blank | Missing intermediate tree nodes or mismatched userRouteAuth codes |
| Every API returns to login | Check neutralize; nested fields may need a recursive walk |
| No WS frames | Subscription may require interaction; increase perRouteMs |
| Empty response bodies | Real bodies require forward=true |
| Chromium missing | Install/configure config.chromium or CHROMIUM |
| Mocks still redirect to login | Hook too late or missing location.href interception; inject at document-start |
| Empty lists | Expected L3 empty arrays; continue tabs/settings/details |
| Redux actions mistaken for routes | Filter internal get/set/change/clear/toggle/upload paths |
| Vue still redirects | Correct injection timing or handle guards when neutralizeVueRouter=false |
| Response URLs absent from log | Enable extractUrlsFromResponse or inspect detail records |
| Unknown Authorization header | Enable observe.xhrHeaders or inspect Network headers |
| Slow/timeouts | domcontentloaded, lower routeTimeout, avoid networkidle2 |
| Proxy failure | Check config/environment; match Puppeteer and curl proxy ports |

## G. Hardened targets

Server-validated sessions (unforgeable signed cookies or server-rendered menus that cannot be stubbed) may block runtime at the shell. Static endpoint enumeration remains possible because module paths are still in code. Where authorization permits an existing real session, the same harness can capture real methods/parameters/responses with forward=true and no neutralization; respect the main skill's authentication boundary.

## H. Per-task checklist

1. Confirm authorization.
2. Read/adapt/run harvest_static.py; review api_static.txt/routes.txt.
3. Phase 1b anchored windows and binding chains → param_candidates.json (J).
4. Reverse engineer A1/A2/A3 → target-specific config.json.
5. Read/adapt runtime_harvest.js/preload.js. Keep TLS verification and Chromium sandbox enabled. Private CAs use caCert/ARTEX_RECON_CA_CERT; only isolated authorized exceptions may use insecureTLS/ARTEX_RECON_INSECURE_TLS. Root browser execution is refused unless allowNoSandbox is explicitly selected for isolation.
6. Depth: npm install, then run the adapted harness.
7. Coverage/both: document-start preload, dynamic enumeration, and parameter matrix.
8. Blank modules: restore permissions in I, patch stubs, rerun.
9. Diff samples and infer requirements from errors → params_merged.json.
10. Merge site_map.json/api_merged.txt and honestly describe coverage, gaps, and script changes.

## I. Permission-tree restoration

Use when a simple menus array with path/show does not mount child modules.

### I1. Locate auth modules

```bash
grep -l 'userRouteAuth' js/*.js
grep -l 'routeMap\|routeLink' js/*.js
grep -rhoaE 'getResultTree|role_permissions|permissions/all' js | head
```

Record permission API paths, response fields, and consuming chunk names.

### I2. Extract routeMap

```bash
python3 scripts/extract_route_map.py recon/js recon/
# Produces recon/route_map.json
```

If no routeMap pattern is found, widen the copied script's regex or inspect:

```bash
grep -rhoaE '([A-Z_][A-Z0-9_]*):\{name:"[^"]*",link:"/[^"]+"\}' js | head -20
```

### I3. Build the tree and stubs

```bash
python3 scripts/build_perm_tree.py recon/js recon/ --config recon/config.json
```

The script parses userRouteAuth (including webpack aliases such as He=o.DASHBOARD), resolves aliases through route_map.json, infers parents from prefixes (MONITOR_ALERT→MONITOR), and writes permissions_tree.json, permissions_all_stub.json, and role_permissions_stub.json. With --config it updates stubs and extends routes.

Adapt DEFAULT_ROOTS (top-level module codes), DEFAULT_PREFIX_PARENT (prefix→parent), and DEFAULT_EXTRA_PARENT (non-prefix orphan relationships).

### I4. Validate consistency

```bash
# Permission count should approximately match userRouteAuth entries
wc -l recon/perm_codes_all.txt
# routes should cover all route_map links
python3 -c "import json; m=json.load(open('recon/route_map.json')); r=set(json.load(open('recon/config.json'))['routes']); print('missing', [v['link'] for v in m.values() if v['link'] not in r])"
```

### I5. Rerun and compare

```bash
node recon/runtime_harvest.js recon/config.json
# Compare runtime_api.json counts before/after mocking; check module APIs for /attack, /asset, etc.
```

| Before | Successful result |
| --- | --- |
| Same 3–5 bootstrap calls per route | Different module APIs by route |
| Only /api/locale/language | Module endpoints such as /api/web/... |
| A few routes | 80–110+ routes recovered from route_map |

### I6. If still blocked

Click sidebar items/tabs in coverage mode; some permission checks trigger only after interaction. Check stub nesting against actual API structure when a permitted session is available. Search hasPermission/checkRole/func. for extra button checks and extend permissions. Static api_static.txt remains the fallback: runtime adds methods/bodies, while parameter candidates and recorded samples remain useful.

## J. Parameter reverse engineering

Methodology, not a universal script: paths use regex; parameters use anchored windows, UI binding chains, multiple-sample diffs, and error inference.

### J1. Anchor on paths to locate payloads

```bash
# Anchor on paths known from Phase 1
grep -n '"/api/user/list"' js/*.js
grep -rhoaE '.{0,120}("/api[^"]+").{0,200}' js | head
grep -rhoaE '(params|data|body|payload)\s*:\s*\{' js | head
grep -rhoaE '(get|post|put|delete|patch)\([^,]+,\s*\{' js | head
```

### J2. Wrappers and transports

```bash
# axios / shared request wrapper
grep -rhoaE '(axios|request)\.(get|post|put|delete|patch)\(' js | head
grep -rhoaE 'interceptors\.(request|response)' js | head

# GraphQL
grep -rhoaE '(query|mutation)\s+\w+|gql`|graphql\(' js | head
grep -rhoaE '\$[a-zA-Z_]+\s*:\s*(Int|String|Boolean|\[)' js | head

# FormData / multipart
grep -rhoaE 'FormData|\.append\(' js | head

# Path parameters
grep -rhoaE 'path:\s*"/[^"]*:[^"]+"' js | head
grep -rhoaE 'useParams|route\.params|\$route\.params' js | head
```

### J3. Validation: required, format, enum

```bash
grep -rhoaE '(required|message|pattern|enum|validator)\s*:' js | head
grep -rhoaE 'yup\.|zod\.|async-validator|Form\.Item|a-form-item|el-form-item' js | head
grep -rhoaE 'rules\s*:\s*\[|name:\s*["\'][a-zA-Z_]+["\']' js | head
grep -rhoaE 'label.*value|options\s*:\s*\[' js | head
```

### J4. Form-to-API binding

```bash
grep -rhoaE 'onFinish|handleSubmit|getFieldsValue|validateFields' js | head
grep -rhoaE '(pick|omit|transform|dayjs|moment)\(' js | head
```

In DevTools Network, follow the Initiator call stack upward from fetch/send to payload construction.

### J5. Encrypted parameters

```bash
grep -rhoaE 'encrypt|decrypt|sign|CryptoJS|sm2|sm3|sm4|RSA|AES' js | head
```

Do not guess fields from ciphertext. Hook encryption-function arguments and record plaintext before encryption. Save conclusions in config.json/param_candidates.json.

### J6. Mandatory Phase 3 parameter matrix

Record each operation per module and diff body/query:

| Action | Evidence |
| --- | --- |
| Initial list | Pagination defaults |
| Search | keyword/filters |
| Advanced filters | Optional fields |
| Create/edit | Complete entity |
| Batch/export | ids[]/exportType |
| Sort/page | sortField/order |

Output param_samples.json entries with path, method, action (such as search), body, query, and headers.

### J7. Confidence

| Level | Condition |
| --- | --- |
| High | Static callsite plus at least two consistent runtime samples |
| Medium | Static-only or one runtime sample |
| Low | Response/error inference without independent verification |
| Pending trigger | Known static field whose UI/permissions were not reached |

### J8. Scenario recipes

| Scenario | Sequence |
| --- | --- |
| REST list | J1 payload → J6 four sample diffs → J3 rules |
| Create/edit form | J3 Form name → J4 submit chain → runtime submit/incomplete request and HTTP 400 |
| GraphQL | J2 variable declarations → variables for each runtime operation |
| Encrypted body | J5 argument hook → plaintext fields are actual parameters |

### J9. Phase mapping

Static Phase 1 uses J1. Phase 2 A2 reveals global tenantId/sign fields. Phase 3 uses J6/param_samples.json. Phase 4 unlocks module-specific forms/fields through permissions. Phase 5 creates params_merged.json with confidence; one sample cannot establish required status.

### J10. Troubleshooting

| Symptom | Action |
| --- | --- |
| Static field never appears at runtime | Mark pending trigger; restore permissions, use advanced filters, exercise dependent select options |
| Same path, different body shapes | Record separately by action; do not force one schema |
| Stubbed response but parameters needed | Inspect real outbound bodies/headers, not mock response contents |
| HTTP 400 references nested fields | Check data/bizData/variables wrappers |
| GraphQL shows only operation name | Expand variables JSON and inspect static $var: Type |
