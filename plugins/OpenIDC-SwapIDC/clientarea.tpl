{literal}<style>
.oidc-panel { border-radius:8px; overflow:hidden; box-shadow:0 2px 12px rgba(0,0,0,.08); margin-bottom:20px; width:100%; box-sizing:border-box; }
.oidc-panel .panel-heading { background:linear-gradient(135deg,#1a73e8,#0d47a1); color:#fff; padding:14px 20px; font-size:15px; border:none; }
.oidc-stat-grid { display:grid; grid-template-columns:repeat(5,1fr); gap:0; }
.oidc-stat-item { padding:18px 10px; text-align:center; border-right:1px solid #f0f0f0; border-bottom:1px solid #f0f0f0; }
.oidc-stat-item:nth-child(5n) { border-right:none; }
.oidc-stat-item .val { font-size:22px; font-weight:700; line-height:1.2; margin-bottom:4px; }
.oidc-stat-item .lbl { font-size:11px; color:#888; text-transform:uppercase; letter-spacing:.5px; }
.oidc-stat-item.c-blue .val  { color:#1a73e8; }
.oidc-stat-item.c-green .val { color:#34a853; }
.oidc-stat-item.c-orange .val{ color:#fa7b17; }
.oidc-stat-item.c-purple .val{ color:#9334e6; }
.oidc-stat-item.c-teal .val  { color:#00897b; }
.oidc-stat-item.c-red .val   { color:#e53935; }
.oidc-stat-item.c-indigo .val{ color:#3949ab; }
.oidc-stat-item.c-amber .val { color:#f9a825; }
.oidc-stat-item.c-cyan .val  { color:#00acc1; }
.oidc-stat-item.c-lime .val  { color:#7cb342; }
.oidc-info-bar { background:#f8f9fa; padding:10px 18px; font-size:12px; color:#666; border-top:1px solid #eee; display:flex; flex-wrap:wrap; gap:16px; }
.oidc-info-bar span { display:flex; align-items:center; gap:4px; }
.oidc-info-bar .tag { background:#e8f0fe; color:#1a73e8; border-radius:4px; padding:1px 7px; font-weight:600; }
.oidc-actions { padding:12px 18px; border-top:1px solid #eee; display:flex; gap:8px; flex-wrap:wrap; }
.oidc-btn { display:inline-flex; align-items:center; gap:5px; padding:7px 16px; border-radius:5px; font-size:13px; font-weight:500; text-decoration:none; cursor:pointer; border:none; transition:all .2s; }
.oidc-btn-primary { background:#1a73e8; color:#fff; }
.oidc-btn-primary:hover { background:#1557b0; color:#fff; }
.oidc-btn-default { background:#fff; color:#444; border:1px solid #ddd; }
.oidc-btn-default:hover { background:#f5f5f5; color:#222; }
@media(max-width:600px){ .oidc-stat-grid{grid-template-columns:repeat(2,1fr);} .oidc-stat-item:nth-child(5n){border-right:1px solid #f0f0f0;} .oidc-stat-item:nth-child(2n){border-right:none;} }
</style>{/literal}

<div class="panel panel-default oidc-panel">
  <div class="panel-heading">
    <strong>&#128421; {$plang['虚拟机信息']}</strong>
  </div>
  <div class="panel-body" style="padding:0;">

    <div class="oidc-stat-grid">
      <div class="oidc-stat-item c-blue">
        <div class="val">{$openidc['cpu']}</div>
        <div class="lbl">{$plang['CPU / 核']}</div>
      </div>
      <div class="oidc-stat-item c-green">
        <div class="val">{$openidc['mem']}</div>
        <div class="lbl">{$plang['内存 / MB']}</div>
      </div>
      <div class="oidc-stat-item c-orange">
        <div class="val">{$openidc['hdd']}</div>
        <div class="lbl">{$plang['硬盘 / MB']}</div>
      </div>
      <div class="oidc-stat-item c-purple">
        <div class="val">{$openidc['os']|truncate:8:''}</div>
        <div class="lbl">{$plang['操作系统']}</div>
      </div>
      <div class="oidc-stat-item c-teal">
        <div class="val">{if $openidc['speed_u'] > 0}{$openidc['speed_u']}{else}∞{/if}</div>
        <div class="lbl">{$plang['上行 / Mbps']}</div>
      </div>
      <div class="oidc-stat-item c-indigo">
        <div class="val">{if $openidc['speed_d'] > 0}{$openidc['speed_d']}{else}∞{/if}</div>
        <div class="lbl">{$plang['下行 / Mbps']}</div>
      </div>
      <div class="oidc-stat-item c-red">
        <div class="val">{$openidc['nat_num']|default:'—'}</div>
        <div class="lbl">{$plang['NAT端口数']}</div>
      </div>
      <div class="oidc-stat-item c-amber">
        <div class="val">{if $openidc['flu_num'] > 0}{$openidc['flu_num']}{else}∞{/if}</div>
        <div class="lbl">{$plang['流量 / GB']}</div>
      </div>
      <div class="oidc-stat-item c-cyan">
        <div class="val">{$openidc['web_num']|default:'—'}</div>
        <div class="lbl">{$plang['Web代理']}</div>
      </div>
      <div class="oidc-stat-item c-lime">
        <div class="val">{if $openidc['gpu_mem'] > 0}{$openidc['gpu_mem']}{else}—{/if}</div>
        <div class="lbl">GPU / MB</div>
      </div>
    </div>

    <div class="oidc-info-bar">
      <span>{$plang['主机']}: <span class="tag">{$openidc['hs_name']}</span></span>
      <span>{$plang['虚拟机']}: <span class="tag">{$openidc['vm_uuid']}</span></span>
    </div>

    <div class="oidc-actions">
      {foreach from=$ClientAreaButtonLink key=k item=v}
        {if $k == $plang['进入控制台'] || $k == '进入控制台'}
          <a href="{$v}" target="_blank" class="oidc-btn oidc-btn-primary">&#128279; {$k}</a>
        {else}
          <a href="{$v}" target="_blank" class="oidc-btn oidc-btn-default">&#128279; {$k}</a>
        {/if}
      {/foreach}
    </div>

  </div>
</div>