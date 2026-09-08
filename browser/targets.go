package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/chromedp/chromedp"
)

// This is a deliberately compact DOM view, not a complete accessibility-tree
// implementation. Keep naming/role rules shared by snapshots and targeting.
const targetHelpersJS = `
 const norm=s=>String(s||'').trim().replace(/\s+/g,' ');
 const visible=e=>{const s=getComputedStyle(e),r=e.getBoundingClientRect();return s.visibility!=='hidden'&&s.visibility!=='collapse'&&s.display!=='none'&&r.width>0&&r.height>0;};
 const role=e=>e.getAttribute('role')||({BUTTON:'button',SELECT:e.multiple?'listbox':'combobox',TEXTAREA:'textbox',IMG:'img',SUMMARY:'button'}[e.tagName]||(e.tagName==='A'&&e.hasAttribute('href')?'link':'')||(e.tagName==='INPUT'?({checkbox:'checkbox',radio:'radio',button:'button',submit:'button',range:'slider',number:'spinbutton',search:'searchbox'}[e.type]||'textbox'):'')||(/^H[1-6]$/.test(e.tagName)?'heading':''));
 const name=e=>{const ids=e.getAttribute('aria-labelledby');if(ids){const text=norm(ids.split(/\s+/).map(id=>document.getElementById(id)?.textContent||'').join(' '));if(text)return text;}return norm(e.getAttribute('aria-label')||(e.labels&&[...e.labels].map(l=>l.innerText).join(' '))||e.getAttribute('alt')||((e.tagName==='INPUT'&&['button','submit'].includes(e.type))?e.value:'')||e.innerText||e.getAttribute('title')||'');};
 const fingerprint=e=>[e.tagName,e.getAttribute('type'),role(e),name(e),e.getAttribute('href')].join('\u0001');
`

func (s *session) snapshot(ctx context.Context) (any, error) {
	t, err := s.selectedTab()
	if err != nil {
		return nil, err
	}
	s.refCounter++
	prefix, _ := json.Marshal(fmt.Sprintf("s%de", s.refCounter))
	script := `(()=>{` + targetHelpersJS + `
 const prefix=` + string(prefix) + `;const refs=new Map(),out=[];let n=0,total=0;
 for(const e of document.querySelectorAll('a[href],button,input,textarea,select,[role],[contenteditable="true"],summary,h1,h2,h3,h4,h5,h6')){
  if(!visible(e))continue;total++;if(out.length>=200)continue;
  const ref=prefix+(++n);refs.set(ref,{element:e,fingerprint:fingerprint(e)});
  const item={ref,role:role(e),name:name(e).slice(0,200),disabled:!!e.disabled||e.getAttribute('aria-disabled')==='true'};
  // Never include password values, or fallback to a textbox value as its name.
  if(e.tagName==='INPUT'&&e.type==='password')item.value='[redacted]';
  else if('value' in e)item.value=String(e.value).slice(0,200);
  out.push(item);
 }
 window.__tedSnapshot={refs};
 const text=(document.body?.innerText||'');
 return {url:location.href,title:document.title,text:text.slice(0,12000),elements:out,truncated:text.length>12000||total>out.length};
})()`
	var result any
	if err := s.runTab(ctx, t, chromedp.Evaluate(script, &result)); err != nil {
		return nil, err
	}
	return result, nil
}

var snapshotRefPattern = regexp.MustCompile(`^s[0-9]+e[0-9]+$`)

func (s *session) resolveSelector(ctx context.Context, t *browserTab, params map[string]any, required bool) (string, error) {
	target := map[string]string{}
	count := 0
	for _, key := range []string{"selector", "ref", "label", "role", "name"} {
		value, err := stringParam(params, key, false)
		if err != nil {
			return "", err
		}
		target[key] = value
		if key != "name" && value != "" {
			count++
		}
	}
	if count > 1 {
		return "", fail("invalid_params", "choose only one of selector, ref, label, or role")
	}
	if target["name"] != "" && target["role"] == "" {
		return "", fail("invalid_params", "name requires role")
	}
	if count == 0 {
		if required {
			return "", fail("invalid_params", "a selector, ref, label, or role is required")
		}
		return "", nil
	}
	if target["ref"] != "" && !snapshotRefPattern.MatchString(target["ref"]) {
		return "", fail("stale_ref", "reference is invalid; take a fresh snapshot")
	}
	s.refCounter++
	marker := fmt.Sprintf("tedq%d", s.refCounter)
	target["marker"] = marker
	payload, _ := json.Marshal(target)
	script := `(()=>{` + targetHelpersJS + `
 const q=` + string(payload) + `;let matches=[];
 if(q.ref){const saved=window.__tedSnapshot?.refs?.get(q.ref);if(!saved||!saved.element.isConnected||saved.fingerprint!==fingerprint(saved.element))return {code:'stale_ref'};matches=[saved.element];}
 else if(q.selector){try{matches=[...document.querySelectorAll(q.selector)];}catch(e){return {code:'invalid_selector'};}}
 else{matches=[...document.querySelectorAll('a,button,input,textarea,select,[role],[contenteditable="true"],summary,h1,h2,h3,h4,h5,h6')].filter(e=>(!q.role||role(e)===q.role)&&(!q.name||name(e)===norm(q.name))&&(!q.label||name(e)===norm(q.label)));}
 matches=matches.filter(visible);
 if(matches.length>1)return {code:'target_ambiguous',count:matches.length};
 if(matches.length===0)return {code:'target_missing'};
 matches[0].setAttribute('data-ted-query',q.marker);return {code:'ok'};
})()`
	for {
		var result struct {
			Code  string `json:"code"`
			Count int    `json:"count"`
		}
		if err := s.runTab(ctx, t, chromedp.Evaluate(script, &result)); err != nil {
			return "", err
		}
		switch result.Code {
		case "ok":
			return `[data-ted-query="` + marker + `"]`, nil
		case "stale_ref":
			return "", fail("stale_ref", "reference no longer identifies the observed element; take a fresh snapshot")
		case "target_ambiguous":
			return "", fail("target_ambiguous", "target matched %d visible elements; use a more specific target", result.Count)
		case "invalid_selector":
			return "", fail("invalid_params", "invalid CSS selector")
		case "target_missing":
		default:
			return "", fail("action_failed", "could not resolve target")
		}
		if err := waitTargetPoll(ctx); err != nil {
			return "", err
		}
	}
}

func waitTargetPoll(ctx context.Context) error {
	timer := time.NewTimer(50 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Check visibility, enabled state and occlusion before dispatching an action.
// Only observation is retried; a click or form change is sent at most once.
func (s *session) actionableSelector(ctx context.Context, t *browserTab, params map[string]any) (string, error) {
	sel, err := s.resolveSelector(ctx, t, params, true)
	if err != nil {
		return "", err
	}
	quoted, _ := json.Marshal(sel)
	script := `(()=>{const e=document.querySelector(` + string(quoted) + `);if(!e||!e.isConnected)return 'missing';e.scrollIntoView({block:'center',inline:'center',behavior:'instant'});if(e.disabled||e.getAttribute('aria-disabled')==='true')return 'blocked';const r=e.getBoundingClientRect(),x=Math.max(0,Math.min(innerWidth-1,r.left+r.width/2)),y=Math.max(0,Math.min(innerHeight-1,r.top+r.height/2)),top=document.elementFromPoint(x,y);return top&&(top===e||e.contains(top))?'ok':'blocked';})()`
	for {
		var state string
		if err := s.runTab(ctx, t, chromedp.Evaluate(script, &state)); err != nil {
			return "", err
		}
		if state == "ok" {
			return sel, nil
		}
		if state == "missing" {
			return "", fail("target_missing", "target was detached before action")
		}
		if err := waitTargetPoll(ctx); err != nil {
			return "", err
		}
	}
}

func (s *session) click(ctx context.Context, params map[string]any) (any, error) {
	t, err := s.selectedTab()
	if err != nil {
		return nil, err
	}
	sel, err := s.actionableSelector(ctx, t, params)
	if err != nil {
		return nil, err
	}
	if err := s.runTab(ctx, t, chromedp.Click(sel, chromedp.ByQuery)); err != nil {
		return nil, err
	}
	return map[string]any{"clicked": true}, nil
}

func requiredValue(params map[string]any) (string, error) {
	if _, ok := params["value"]; !ok {
		return "", fail("invalid_params", "value is required (and may be empty)")
	}
	value, ok := params["value"].(string)
	if !ok {
		return "", fail("invalid_params", "value must be a string")
	}
	return value, nil
}

func (s *session) fill(ctx context.Context, params map[string]any) (any, error) {
	return s.setControlValue(ctx, params, false)
}
func (s *session) selectValue(ctx context.Context, params map[string]any) (any, error) {
	return s.setControlValue(ctx, params, true)
}

func (s *session) setControlValue(ctx context.Context, params map[string]any, selectOnly bool) (any, error) {
	value, err := requiredValue(params)
	if err != nil {
		return nil, err
	}
	t, err := s.selectedTab()
	if err != nil {
		return nil, err
	}
	sel, err := s.actionableSelector(ctx, t, params)
	if err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(map[string]any{"selector": sel, "value": value, "selectOnly": selectOnly})
	script := `(()=>{const q=` + string(payload) + `,e=document.querySelector(q.selector);if(!e)return 'missing';
 if(q.selectOnly&&e.tagName!=='SELECT')return 'not_select';
 if(q.selectOnly&&![...e.options].some(o=>o.value===q.value&&!o.disabled))return 'option_missing';
 if(!q.selectOnly&&!['INPUT','TEXTAREA'].includes(e.tagName)&&!e.isContentEditable)return 'not_editable';
 if(e.disabled||e.readOnly)return 'blocked';e.focus();
 if(e.isContentEditable)e.textContent=q.value;
 else{const proto=e.tagName==='TEXTAREA'?HTMLTextAreaElement.prototype:e.tagName==='SELECT'?HTMLSelectElement.prototype:HTMLInputElement.prototype;const setter=Object.getOwnPropertyDescriptor(proto,'value').set;setter.call(e,q.value);}
 e.dispatchEvent(new Event('input',{bubbles:true}));e.dispatchEvent(new Event('change',{bubbles:true}));return 'ok';})()`
	var state string
	if err := s.runTab(ctx, t, chromedp.Evaluate(script, &state)); err != nil {
		return nil, err
	}
	switch state {
	case "ok":
		return map[string]any{"updated": true}, nil
	case "missing":
		return nil, fail("target_missing", "target detached before update")
	default:
		return nil, fail("invalid_target", "cannot update control: %s", state)
	}
}
