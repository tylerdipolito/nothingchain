'use strict';
const $ = id => document.getElementById(id);
const state = {health:null,stats:null,peers:[],blocks:[],account:null,tokens:[],tokenStateRoot:'',limit:10,query:'',tip:null,online:false,lastUpdated:0,clockOffset:0,loading:false,selected:null};
const text = (id,value) => { $(id).textContent=value; };
const el = (tag,className,value) => { const n=document.createElement(tag); if(className)n.className=className; if(value!==undefined)n.textContent=value; return n; };
const short = (s,start=9,end=6) => s && s.length>start+end+3 ? s.slice(0,start)+'…'+s.slice(-end) : (s||'—');
const amount = value => { const [whole,fraction]=String(value??'0').split('.'); return whole.replace(/\B(?=(\d{3})+(?!\d))/g,',')+(fraction?'.'+fraction:''); };
const number = value => new Intl.NumberFormat().format(value);
const dateTime = timestamp => new Date(timestamp*1000).toLocaleString(undefined,{dateStyle:'medium',timeStyle:'medium'});
const now = () => Date.now()+state.clockOffset;
function age(timestamp){ const seconds=Math.max(0,Math.floor((now()-timestamp*1000)/1000)); if(seconds<5)return 'Just now'; if(seconds<60)return seconds+'s ago'; if(seconds<3600)return Math.floor(seconds/60)+'m ago'; if(seconds<86400)return Math.floor(seconds/3600)+'h ago'; return Math.floor(seconds/86400)+'d ago'; }
function badge(node,label,kind=''){node.textContent=label;node.className='status-pill'+(kind?' '+kind:'');}
async function api(path){ const response=await fetch(path,{cache:'no-store',signal:AbortSignal.timeout(8000)});if(!response.ok)throw new Error('Node request failed ('+response.status+')');return response.json(); }
async function postApi(path,body){ const response=await fetch(path,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)}); if(!response.ok){ const err=await response.text(); throw new Error(err||'Request failed ('+response.status+')'); } return response.json(); }
function toast(message){text('toast',message);$('toast').hidden=false;clearTimeout(toast.timer);toast.timer=setTimeout(()=>{$('toast').hidden=true;},3500);}
async function copyWallet(){if(!state.health)return;try{await navigator.clipboard.writeText(state.health.miner);toast('Wallet address copied');}catch{toast('Copy unavailable. Select the wallet address to copy it.');}}
function renderOverview(){
 const {health:h,stats:s,account:a}=state;
 text('balance',amount(a.balance));text('available','Available '+amount(a.available_balance)+' GO');
 text('walletAddress',h.miner);$('walletAddress').title=h.miner;$('copyWallet').disabled=false;
 text('height',number(s.height));text('blockCount',number(s.blocks)+' validated blocks, including genesis');
 text('supply',amount(s.total_supply));text('pending',number(s.pending_txs));text('pendingCaption',s.pending_txs?'Waiting to be included in a block':'All caught up. No transfers waiting.');
 text('miningReward',amount(s.block_reward)+' GO');
 const interval=h.mining_interval; text('miningInterval',interval==='1m0s'?'Every minute':interval);
 const next=new Date(s.next_halving);text('halving',Number.isNaN(next.getTime())?'—':next.toLocaleDateString(undefined,{month:'short',day:'numeric',year:'numeric',timeZone:'UTC'}));
 badge($('chainValidity'),s.valid?'Chain valid':'Validation failed',s.valid?'good':'bad');
 text('nodeLabel',location.hostname+':'+(location.port||'80'));
 renderCountdown();
}
function intervalSeconds(value){let total=0;for(const match of String(value).matchAll(/(\d+(?:\.\d+)?)(h|m|s)/g)){total+=Number(match[1])*({h:3600,m:60,s:1}[match[2]]);}return total||60;}
function renderCountdown(){
 const h=state.health;if(!h)return;
 if(!state.online){badge($('miningBadge'),'Unavailable','warning');text('countdown','—:—');text('miningDescription','Reconnect to read the live schedule.');$('ringProgress').style.strokeDashoffset='100';return;}
 const m=h.mining;
 if(!h.automatic_mining){badge($('miningBadge'),'Validator');text('countdown','Standby');text('countdownLabel','This node validates blocks');text('miningDescription','Automatic mining is off on this node.');$('ringProgress').style.strokeDashoffset='100';return;}
 if(m?.last_error){badge($('miningBadge'),'Mining error','bad');text('countdownLabel','Next scheduled retry');text('miningDescription',m.last_error);}
 else{badge($('miningBadge'),m?.active?'Mining now':'Running','good');text('countdownLabel','Next scheduled attempt');text('miningDescription','Scheduled proof-of-work · fixed difficulty');}
 if(m?.active){text('countdown','Mining…');$('ringProgress').style.strokeDashoffset='0';return;}
 const next=Date.parse(m?.next_attempt||'');
 if(!Number.isFinite(next)||next<1){text('countdown','—:—');return;}
 const remaining=Math.max(0,Math.ceil((next-now())/1000));
 text('countdown',remaining?String(Math.floor(remaining/60)).padStart(2,'0')+':'+String(remaining%60).padStart(2,'0'):'Due now');
 const fraction=Math.min(1,remaining/intervalSeconds(h.mining_interval));$('ringProgress').style.strokeDashoffset=String(fraction*100);
}
function renderTrack(){
 const track=$('blockTrack');track.replaceChildren();
 for(const block of state.blocks.slice().reverse().slice(0,7)){
  const button=el('button','block-tile');button.type='button';button.setAttribute('aria-label','View block '+block.index);
  const top=el('span',null,block.index===0?'GENESIS':'BLOCK');top.append(el('span',null,'◇'));
  button.append(top,el('strong',null,'#'+number(block.index)),el('small',null,block.reward+' GO · '+block.txs.length+' tx'));
  button.addEventListener('click',()=>openBlock(block.index));track.append(button);
 }
}
function filteredBlocks(){const q=state.query.trim().toLowerCase().replace(/^#/,'');return state.blocks.slice().reverse().filter(b=>!q||String(b.index)===q||b.hash.toLowerCase().includes(q)||b.miner.toLowerCase().includes(q));}
function renderTable(){
 const rows=$('blockRows');rows.replaceChildren();const filtered=filteredBlocks();
 for(const b of filtered.slice(0,state.limit)){
  const row=el('tr');const first=el('td');const button=el('button','row-block','#'+number(b.index));button.type='button';button.addEventListener('click',()=>openBlock(b.index));first.append(button);
  const mined=el('td',null,age(b.timestamp));mined.title=dateTime(b.timestamp);
  const txs=el('td',null,String(b.txs.length));
  const miner=el('td');const minerCode=el('code',null,b.index===0?'Genesis':short(b.miner));minerCode.title=b.miner;miner.append(minerCode);
  const reward=el('td',null,amount(b.reward)+' GO');const hash=el('td');const hashCode=el('code',null,short(b.hash,8,6));hashCode.title=b.hash;hash.append(hashCode);
  const end=el('td');const open=el('button','row-open','↗');open.type='button';open.setAttribute('aria-label','Open block '+b.index);open.addEventListener('click',()=>openBlock(b.index));end.append(open);
  row.append(first,mined,txs,miner,reward,hash,end);rows.append(row);
 }
 if(!filtered.length){const row=el('tr');const cell=el('td','empty','No blocks match your search. Try a height, hash, or miner address.');cell.colSpan=7;row.append(cell);rows.append(row);}
 text('resultCount','Showing '+Math.min(state.limit,filtered.length)+' of '+number(filtered.length)+' blocks');$('loadMore').hidden=state.limit>=filtered.length;
}
function nodeCard(title,status,kind,items,bottom){
 const card=el('article','panel node-card');const heading=el('div','node-title');const h=el('h3');h.append(el('span',null,'◎'),document.createTextNode(title));const pill=el('span','status-pill');badge(pill,status,kind);heading.append(h,pill);
 const data=el('div','node-data');for(const [label,value] of items){const d=el('div');d.append(el('small',null,label),el('strong',null,value));data.append(d);}
 card.append(heading,data,el('div','node-bottom',bottom));return card;
}
function peerState(peer){if(peer.last_error)return ['Disconnected','bad'];if(!peer.last_sync||now()-Date.parse(peer.last_sync)>15000)return ['Awaiting sync','warning'];if(peer.remote_tip===state.stats.tip_hash)return ['In sync','good'];return ['Synchronizing','warning'];}
function renderNetwork(){
 const cards=$('networkCards');cards.replaceChildren();const s=state.stats,h=state.health;
 cards.append(nodeCard('This node · '+(location.port||'80'),s.valid?'Validating':'Invalid',s.valid?'good':'bad',[['ROLE',h.automatic_mining?'Miner + validator':'Validator'],['HEIGHT','#'+s.height],['STATUS',state.online?'Online':'Unavailable']],location.origin+' · '+short(s.tip_hash,12,8)));
 let agreed=0;
 for(const peer of state.peers){const [label,kind]=peerState(peer);if(label==='In sync')agreed++;
  const name=peer.url.replace(/^https?:\/\//,'');
  cards.append(nodeCard('Peer · '+name,label,kind,[['HEIGHT',peer.last_sync?'#'+peer.remote_height:'—'],['ROUND TRIP',peer.last_sync?peer.round_trip_ms+' ms':'—'],['CHAIN UPDATES',String(peer.adopted_chains)]],peer.last_error|| (peer.last_sync?'Last successful sync '+new Date(peer.last_sync).toLocaleTimeString()+' · '+short(peer.remote_tip,12,8):'Waiting for the first successful connection.')));
 }
 if(!state.peers.length)cards.append(nodeCard('No peers configured','Standalone','',[['THIS NETWORK','1 local node'],['DISCOVERY','Manual'],['PEERS','0']],'Start a second node and configure --peers to compare chain tips.'));
 text('networkSummary',state.peers.length?agreed+' / '+state.peers.length+' peers agree on the latest block':'Running independently');
}
function detailField(label,value){const n=el('div');n.append(el('small',null,label),el('strong',null,value));return n;}
function hashField(label,value){const n=el('div','hash-field');n.append(el('small',null,label),el('code',null,value));return n;}
function renderDialog(){
 const block=state.blocks.find(b=>b.index===state.selected);if(!block)return;
 text('dialogTitle','Block #'+block.index);const target=$('blockDetails');target.replaceChildren();
 const grid=el('div','detail-grid');grid.append(detailField('MINING REWARD',block.reward+' GO'),detailField('TRANSACTIONS',String(block.txs.length)),detailField('CONFIRMATIONS',String(state.stats.height-block.index+1)));
 target.append(grid,hashField('BLOCK HASH',block.hash),hashField('PREVIOUS BLOCK',block.prev_hash),hashField('MINER',block.miner||'Genesis — no reward wallet'));
 const meta=el('p','dialog-note','Mined '+dateTime(block.timestamp)+' · Nonce '+block.nonce);target.append(meta);
 const transfers=el('section','dialog-section');transfers.append(el('h3',null,'Transfers in this block'));
 if(!block.txs.length)transfers.append(el('p','dialog-note',block.index===0?'Genesis contains no transfers and mints no GO.':'No transfers in this block. Its mining reward is recorded in the block header.'));
 for(const tx of block.txs){const card=el('article','transfer-card');card.append(el('strong',null,tx.amount+' GO'),el('p',null,'From: '+tx.from),el('p',null,'To: '+tx.to),el('p',null,'Nonce: '+tx.nonce));if(tx.memo)card.append(el('p',null,'Memo: '+tx.memo));transfers.append(card);}
 target.append(transfers,el('p','dialog-note','Confirmations count blocks on this node’s current branch. Competing branches can reorganize this experimental chain.'));
}
function renderTokens(){
 const s=state.stats;
 text('tokensStateRoot',state.tokenStateRoot?'Root: '+short(state.tokenStateRoot,8,6):'Root: None');
 text('tokensBurnedBadge','Burned: '+(s?.burned||'0')+' GO');
 const select=$('transferTokenSelect');if(select){const curVal=select.value;select.replaceChildren();
 if(!state.tokens.length){const opt=el('option',null,'No tokens registered yet');opt.value='';select.append(opt);}
 else{for(const t of state.tokens){const opt=el('option',null,t.symbol+' ('+t.name+')');opt.value=t.id;if(t.id===curVal)opt.selected=true;select.append(opt);}}}
 const rows=$('tokenRows');if(rows){rows.replaceChildren();
 for(const t of state.tokens){
  const row=el('tr');const idCell=el('td');const idCode=el('code',null,short(t.id,12,6));idCode.title=t.id;idCell.append(idCode);
  const symCell=el('td',null,t.symbol);const nameCell=el('td',null,t.name);const decCell=el('td',null,String(t.decimals));
  const supplyCell=el('td',null,amount(t.total_supply));const authCell=el('td');const authCode=el('code',null,t.mint_authority?short(t.mint_authority,8,6):'REVOKED');if(t.mint_authority)authCode.title=t.mint_authority;authCell.append(authCode);
  const createdCell=el('td',null,'#'+t.created_at);row.append(idCell,symCell,nameCell,decCell,supplyCell,authCell,createdCell);rows.append(row);
 }
 if(!state.tokens.length){const row=el('tr');const cell=el('td','empty','No GRC-20 tokens created yet. Use the form above to issue your first asset!');cell.colSpan=7;row.append(cell);rows.append(row);}
 text('tokenResultCount',state.tokens.length+' tokens registered');}
}
function openBlock(index){state.selected=index;renderDialog();if(!$('blockDialog').open)$('blockDialog').showModal();}
async function refresh(){
 if(state.loading)return;state.loading=true;$('refresh').disabled=true;
 try{
  const [health,stats,peers]=await Promise.all([api('/health'),api('/stats'),api('/peers')]);
  let tokenListRes={tokens:[]},stateRootRes={state_root:''};
  try{ [tokenListRes,stateRootRes]=await Promise.all([api('/token/list'),api('/token/state-root')]); }catch(e){}
  state.tokens=tokenListRes.tokens||[];
  state.tokenStateRoot=stateRootRes.state_root||'';
  let blocks=state.blocks;
  if(state.tip!==stats.tip_hash){const result=await api('/explorer/blocks');if(result.tip_hash!==stats.tip_hash)return;blocks=result.blocks;}
  const account=await api('/balance/'+encodeURIComponent(health.miner));
  state.clockOffset=Date.parse(health.server_time)-Date.now();state.health=health;state.stats=stats;state.peers=peers;state.blocks=blocks;state.account=account;state.tip=stats.tip_hash;state.online=true;state.lastUpdated=Date.now();
  $('errorBanner').hidden=true;$('connection').className='live-badge online';$('connection').replaceChildren(el('i'),document.createTextNode('Live'));
  renderOverview();renderTrack();renderTable();renderNetwork();renderTokens();if($('blockDialog').open)renderDialog();text('updated','Updated '+new Date().toLocaleTimeString());
 }catch(error){
  state.online=false;$('errorBanner').hidden=false;text('errorBanner',state.lastUpdated?'Connection lost. Showing the last successful snapshot; retrying automatically.':'Cannot reach this node. Start GoChain and this page will reconnect automatically.');
  $('connection').className='live-badge offline';$('connection').replaceChildren(el('i'),document.createTextNode('Offline'));renderCountdown();
 }finally{state.loading=false;$('refresh').disabled=false;}
}
async function poll(){await refresh();setTimeout(poll,3000);}
$('refresh').addEventListener('click',refresh);$('copyWallet').addEventListener('click',copyWallet);
$('search').addEventListener('input',event=>{state.query=event.target.value;state.limit=10;renderTable();});
$('loadMore').addEventListener('click',()=>{state.limit+=10;renderTable();});
$('closeDialog').addEventListener('click',()=>$('blockDialog').close());
$('blockDialog').addEventListener('click',event=>{if(event.target===$('blockDialog')){const r=$('blockDialog').getBoundingClientRect();if(event.clientX<r.left||event.clientX>r.right||event.clientY<r.top||event.clientY>r.bottom)$('blockDialog').close();}});
for(const link of document.querySelectorAll('.nav-link'))link.addEventListener('click',()=>{for(const other of document.querySelectorAll('.nav-link'))other.classList.toggle('active',other===link);});
$('createTokenForm')?.addEventListener('submit',async event=>{
 event.preventDefault();
 try{
  const name=$('tokenName').value.trim();
  const symbol=$('tokenSymbol').value.trim().toUpperCase();
  const decimals=Number($('tokenDecimals').value);
  const initial_supply=Number($('tokenInitial').value);
  const max_supply=Number($('tokenMax').value);
  await postApi('/token/create',{wallet:".gochain-wallet.json",name,symbol,decimals,initial_supply,max_supply});
  toast('Token '+symbol+' creation queued! Mine a block to confirm.');
  $('tokenName').value='';$('tokenSymbol').value='';refresh();
 }catch(err){toast('Error: '+err.message);}
});
$('transferTokenForm')?.addEventListener('submit',async event=>{
 event.preventDefault();
 try{
  const token_id=$('transferTokenSelect').value;
  if(!token_id){toast('Please select a token');return;}
  const recipient=$('transferRecipient').value.trim();
  const amount=Number($('transferAmount').value);
  await postApi('/token/transfer',{wallet:".gochain-wallet.json",token_id,recipient,amount});
  toast('Transfer of '+amount+' tokens queued! Mine a block to confirm.');
  $('transferAmount').value='';refresh();
 }catch(err){toast('Error: '+err.message);}
});
setInterval(renderCountdown,1000);poll();
