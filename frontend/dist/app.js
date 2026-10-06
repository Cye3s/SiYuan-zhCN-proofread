/* SiYuan-zhCN-proofread 思源简中校对 · 前端逻辑（Vue3 组合式）
   列随数据渐进显示：导入 zh-CN.json 后才显示简中列与校对列。 */
const { createApp, ref, reactive, computed, onMounted, onBeforeUnmount, nextTick } = Vue;

createApp({
  setup() {
    /* ---------------- 常量 ---------------- */
    const p_lineHeight = 19;    // 单行高度 px
    const p_padHeight = 8;      // 单行上下边距 px
    const p_scrollBuffer = 60;  // 虚拟滚动上下缓冲 px
    const p_maxLines = 3;       // 单元格最多显示行数（超出截断，估算保守不重叠）
    const p_charWidth = 13;     // 全角字符近似宽度 px

    /* ---------------- 状态 ---------------- */
    const v_rows = ref([]);
    const v_dirty = reactive(new Map());      // key -> 校对值（未保存；与简中相同也存实际值）
    const v_batchUndo = reactive(new Map()); // 本轮批量替换撤销快照
    const v_batchKeys = ref(null);            // 批量替换后过滤显示的 key 集合

    const v_searchInput = ref('');
    const v_search = ref('');
    const v_onlyNew = ref(false);
    const v_onlyPending = ref(false);
    const v_showObsolete = ref(false);
    const v_searchCols = reactive({ key: true, tw: true, cn: true }); // 查找范围（可多选）

    const v_editKey = ref(null);
    const v_editText = ref('');

    const v_rep = reactive({ src: '', dst: '', cs: false });
    const v_repPreview = ref([]);
    const v_replaceHistory = ref(JSON.parse(localStorage.getItem('syfix_hist') || '[]'));

    const v_modal = reactive({ replace: false, sync: false, export: false, import: false });
    const v_syncSummary = ref({ added: 0, removed: 0, changed: 0, renamed: [] });
    const v_importTitle = ref('');
    const v_importMsg = ref('');
    const v_exportResult = ref({});
    const v_toast = ref('');

    const v_scrollEl = ref(null), v_searchEl = ref(null), v_fileEl = ref(null);
    let v_fileRole = 'cn';
    const v_scrollTop = ref(0), v_viewHeight = ref(600), v_contWidth = ref(1000);

    /* ---------------- 工具函数 ---------------- */
    const esc = p_text => String(p_text).replace(/[&<>"]/g, v_ch =>
      ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[v_ch]));

    function hl(p_text, p_keyword) {          // 命中词黄底高亮
      const v_text = p_text == null ? '' : String(p_text);
      if (!p_keyword) return esc(v_text);
      const v_low = v_text.toLowerCase();
      const v_kw = p_keyword.toLowerCase();
      let v_out = '', v_pos = 0;
      for (;;) {
        const v_hit = v_low.indexOf(v_kw, v_pos);
        if (v_hit < 0) { v_out += esc(v_text.slice(v_pos)); break; }
        v_out += esc(v_text.slice(v_pos, v_hit))
          + '<mark>' + esc(v_text.slice(v_hit, v_hit + p_keyword.length)) + '</mark>';
        v_pos = v_hit + p_keyword.length;
      }
      return v_out;
    }

    function diffSegs(p_old, p_new) {  // 前后缀剥离，返回差异段
      const v_old = p_old || '';
      const v_new = p_new || '';
      if (v_old === v_new) return [{ t: 'eq', s: v_new }];
      let v_pre = 0;
      while (v_pre < v_old.length && v_pre < v_new.length && v_old[v_pre] === v_new[v_pre]) v_pre++;
      let v_end = v_old.length, v_endNew = v_new.length;
      while (v_end > v_pre && v_endNew > v_pre && v_old[v_end - 1] === v_new[v_endNew - 1]) { v_end--; v_endNew--; }
      return [
        { t: 'eq', s: v_old.slice(0, v_pre) },
        { t: 'del', s: v_old.slice(v_pre, v_end) },
        { t: 'ins', s: v_new.slice(v_pre, v_endNew) },
        { t: 'eq', s: v_old.slice(v_end) },
      ].filter(v_seg => v_seg.s);
    }

    function disp(p_pointer) {               // pointer -> 美化显示（与后端一致）
      const v_parts = String(p_pointer).split('/').filter(v_seg => v_seg !== '');
      let v_out = v_parts[0] || '';
      for (let v_idx = 1; v_idx < v_parts.length; v_idx++) {
        v_out += (/^\d+$/.test(v_parts[v_idx]) ? '_' : '.') + v_parts[v_idx];
      }
      return v_out;
    }

    const v_toastTimer = { id: null };
    function toastMsg(p_msg) {
      v_toast.value = p_msg;
      clearTimeout(v_toastTimer.id);
      v_toastTimer.id = setTimeout(() => v_toast.value = '', 6000);
    }

    /* ---------------- 数据访问 ---------------- */
    const curFix = p_row => v_dirty.has(p_row.key) ? v_dirty.get(p_row.key) : (p_row.fix_cn || null);

    async function reload() {
      const v_res = await fetch('/api/entries');
      const v_data = await v_res.json();
      // 按 zh-CN json 文件中的 key 顺序排序（不在文件中的行由后端排在最后）
      v_rows.value = v_data.rows.slice().sort((a, b) => (a.order - b.order) || (a.key < b.key ? -1 : 1));
      calcKeyCol();
    }

    function persistLocal() {
      localStorage.setItem('syfix_dirty', JSON.stringify([...v_dirty.entries()]));
    }

    /* key 列宽 = 最长 key 字符串实测宽度 */
    function calcKeyCol() {
      const v_ctx = document.createElement('canvas').getContext('2d');
      v_ctx.font = '12px Consolas, monospace';
      let v_max = 0;
      for (const v_row of v_rows.value) {
        const v_width = v_ctx.measureText(v_row.display || '').width;
        if (v_width > v_max) v_max = v_width;
      }
      document.documentElement.style.setProperty('--keycol', (Math.ceil(v_max) + 18) + 'px');
    }

    function keycolPx() {
      return parseInt(document.documentElement.style.getPropertyValue('--keycol')) || 220;
    }

    /* ---------------- 列渐进显示 ---------------- */
    /* 打开 exe 不自动加载数据（空表引导），列显示由本次会话的导入动作驱动；
       库中既有校对成果在「导入zh-CN.json」时经版本同步合并恢复 */
    const v_hasTw = ref(false);
    const v_hasCn = ref(false);

    const v_gridCols = computed(() => {
      const v_cols = [keycolPx() + 'px'];
      if (v_hasTw.value) v_cols.push('1fr');
      if (v_hasCn.value) v_cols.push('1fr', '1.3fr');
      v_cols.push('34px');
      return v_cols.join(' ');
    });

    /* ---------------- 过滤 / 统计 ---------------- */
    const v_filtered = computed(() => {
      let v_list = v_rows.value;
      if (!v_showObsolete.value) v_list = v_list.filter(v_row => v_row.status !== 'obsolete');
      if (v_onlyNew.value) v_list = v_list.filter(v_row => v_row.is_new);
      if (v_onlyPending.value) v_list = v_list.filter(v_row => v_row.status === 'pending' || v_row.status === 'stale');
      if (v_batchKeys.value) v_list = v_list.filter(v_row => v_batchKeys.value.has(v_row.key));
      if (v_search.value) {
        const v_kw = v_search.value.toLowerCase();
        v_list = v_list.filter(v_row => hitRow(v_row, v_kw));
      }
      return v_list;
    });

    // 按所选列命中：key / zh-TW / zh-CN（校对列内容同为简中文本，随 zh-CN 一并查找）
    function hitRow(p_row, p_kw) {
      if (v_searchCols.key) {
        if ((p_row.display || '').toLowerCase().includes(p_kw) ||
            (p_row.key || '').toLowerCase().includes(p_kw)) return true;
      }
      if (v_searchCols.tw && (p_row.zh_tw || '').toLowerCase().includes(p_kw)) return true;
      if (v_searchCols.cn) {
        if ((p_row.zh_cn || '').toLowerCase().includes(p_kw) ||
            ((curFix(p_row) ?? '') || '').toLowerCase().includes(p_kw)) return true;
      }
      return false;
    }

    const colsAny = computed(() => v_searchCols.key || v_searchCols.tw || v_searchCols.cn);
    const colsSummary = computed(() => {
      const v_names = [];
      if (v_searchCols.key) v_names.push('key');
      if (v_searchCols.tw) v_names.push('zh-TW');
      if (v_searchCols.cn) v_names.push('zh-CN');
      if (v_names.length === 3) return '全选';
      return v_names.length ? v_names.join('+') : '未选列';
    });
    function setCols(p_all) {
      v_searchCols.key = v_searchCols.tw = v_searchCols.cn = p_all;
    }

    const v_pendingCount = computed(() =>
      v_rows.value.filter(v_row => v_row.status === 'pending' || v_row.status === 'stale').length);

    function doSearch() { v_search.value = v_searchInput.value.trim(); scrollTopTo(0); }
    function clearFilter() {
      v_search.value = ''; v_searchInput.value = '';
      v_onlyNew.value = false;
      v_onlyPending.value = false;
      v_batchKeys.value = null;
      v_showObsolete.value = false;
      scrollTopTo(0);
    }
    function scrollTopTo(p_top) {
      if (v_scrollEl.value) v_scrollEl.value.scrollTop = p_top;
      v_scrollTop.value = p_top;
    }

    /* ---------------- 虚拟滚动（行高动态估算，clamp 3 行防重叠） ---------------- */
    function onScroll() { v_scrollTop.value = v_scrollEl.value.scrollTop; }

    function colCaps() {   // 各显示列每行可容纳字符数（按 1/1/1.3 权重分配剩余宽度）
      const v_rest = Math.max(240, v_contWidth.value - keycolPx() - 34);
      const v_weights = [];
      if (v_hasTw.value) v_weights.push(1);           // zh-TW
      if (v_hasCn.value) { v_weights.push(1); v_weights.push(1.3); } // 简中 + 校对
      const v_weightSum = v_weights.reduce((v_acc, v_w) => v_acc + v_w, 0) || 1;
      const v_caps = v_weights.map(v_w =>
        Math.max(8, Math.floor(v_rest * v_w / v_weightSum / p_charWidth)));
      return {
        tw: v_hasTw.value ? v_caps[0] : Infinity,
        off: v_hasCn.value ? v_caps[v_hasTw.value ? 1 : 0] : Infinity,
        fix: v_hasCn.value ? v_caps[v_caps.length - 1] : Infinity,
      };
    }

    function rowH(p_row) {
      const v_caps = colCaps();
      const v_fixText = curFix(p_row) ?? '';
      const v_lines = Math.max(
        Math.ceil((p_row.zh_tw || '').length / v_caps.tw),
        Math.ceil((p_row.zh_cn || '').length / v_caps.off),
        Math.ceil((v_fixText || '').length / v_caps.fix),
        1,
      );
      return Math.min(v_lines, p_maxLines) * p_lineHeight + p_padHeight;
    }

    const v_totalHeight = computed(() => {
      let v_sum = 0;
      for (const v_row of v_filtered.value) v_sum += rowH(v_row);
      return v_sum;
    });

    const v_viewRows = computed(() => {
      const v_list = v_filtered.value;
      if (!v_list.length) return [];
      const v_target = Math.max(0, v_scrollTop.value - p_scrollBuffer);
      let v_idx = 0, v_acc = 0;
      while (v_idx < v_list.length) {
        const v_h = rowH(v_list[v_idx]);
        if (v_acc + v_h > v_target) break;
        v_acc += v_h; v_idx++;
      }
      const v_bottom = v_scrollTop.value + v_viewHeight.value + p_scrollBuffer;
      const v_out = [];
      while (v_idx < v_list.length && v_acc < v_bottom) {
        const v_h = rowH(v_list[v_idx]);
        v_out.push({ ...v_list[v_idx], _top: v_acc, _h: v_h });
        v_acc += v_h; v_idx++;
      }
      return v_out;
    });

    function measure() {
      if (v_scrollEl.value) {
        v_viewHeight.value = v_scrollEl.value.clientHeight;
        v_contWidth.value = v_scrollEl.value.clientWidth;
      }
    }
    window.addEventListener('resize', measure);

    /* ---------------- 单元格渲染 ---------------- */
    function rowClass(p_row) {
      const v_cls = {
        stale: p_row.status === 'stale',
        'status-obsolete': p_row.status === 'obsolete',
      };
      if (p_row.is_new) v_cls.isnew = true; // 新增行整行绿；简繁状态改由行首色块提示
      else if (p_row.simp) v_cls['simp-' + p_row.simp] = true;
      return v_cls;
    }
    function simpColor(p_row) {
      return p_row.simp === 'same' ? '#7fb2f0'
        : p_row.simp === 'match' ? '#e8c95a'
        : p_row.simp === 'diff' ? '#e08080' : '';
    }
    function simpText(p_row) {
      return p_row.simp === 'same' ? '简繁字面相同'
        : p_row.simp === 'match' ? 'OpenCC 转换一致·无需校对'
        : p_row.simp === 'diff' ? '简繁转换不一致·需人工校正' : '';
    }
    function cellHtml(p_row, p_text) { return hl(p_text, v_search.value); }
    function cnTip(p_row) {
      if (p_row.status === 'stale' && p_row.zh_cn_prev)
        return `简中新版已改：${p_row.zh_cn}\n旧简中值：${p_row.zh_cn_prev}`;
      return p_row.zh_cn || '';
    }
    function fixCellHtml(p_row) {
      const v_kw = v_search.value;
      const v_cn = p_row.zh_cn ?? '';
      const v_cur = curFix(p_row);
      const v_edited = v_cur !== null && v_cur !== v_cn;
      let v_html;
      if (!v_edited) {
        // 未修改：第 4 列显示简中的复制值（灰色），保存时也按实际值入库
        v_html = `<span style="color:#999">${hl(v_cn, v_kw)}</span>`;
      } else {
        // 修改过：只显示校对后的词，与简中不同的片段直接红字（不显示旧词）
        v_html = diffSegs(v_cn, v_cur).filter(v_seg => v_seg.t !== 'del').map(v_seg =>
          v_seg.t === 'ins' ? `<span class="ins">${hl(v_seg.s, v_kw)}</span>` : hl(v_seg.s, v_kw)
        ).join('');
      }
      if (v_dirty.has(p_row.key)) v_html += '<span class="dirty-dot" title="未保存"></span>';
      return v_html;
    }

    /* ---------------- 行内编辑 ---------------- */
    function startEdit(p_row) {
      if (p_row.status === 'obsolete') { toastMsg('已废弃行，不可编辑'); return; }
      if (!p_row.zh_cn) { toastMsg('请先导入 zh-CN.json'); return; }
      v_editKey.value = p_row.key;
      v_editText.value = curFix(p_row) ?? p_row.zh_cn;
      nextTick(() => document.querySelector('.edit-box')?.focus());
    }
    function commitEdit() {
      if (v_editKey.value === null) return;
      const v_row = v_rows.value.find(v_item => v_item.key === v_editKey.value);
      if (v_row) {
        // 与简中相同也复制实际值入库（dirty 保存语义）
        v_dirty.set(v_row.key, v_editText.value);
        persistLocal();
      }
      v_editKey.value = null;
    }
    function cancelEdit() { v_editKey.value = null; }
    // blur 延迟判定：焦点仍在编辑面板内（如点击英文参考区）不提交
    function onEditBlur() {
      setTimeout(() => {
        const v_wrap = document.querySelector('.edit-wrap');
        if (v_wrap && v_wrap.contains(document.activeElement)) return;
        commitEdit();
      }, 0);
    }
    function restoreRow(p_row) {
      // 还原 = 第 4 列复制回简中值
      v_dirty.set(p_row.key, p_row.zh_cn ?? '');
      persistLocal();
      toastMsg(`已还原：${p_row.display}`);
    }

    /* ---------------- 批量替换 ---------------- */
    const v_histMap = computed(() => {
      const v_map = {};
      for (const v_hist of v_replaceHistory.value) v_map[v_hist.src] = v_hist.dst;
      return v_map;
    });
    function openReplace() {
      v_repPreview.value = [];
      v_modal.replace = true;
    }
    function escapeReg(p_str) { return p_str.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'); }
    function replaceAllCI(p_text, p_src, p_dst) {
      return p_text.replace(new RegExp(escapeReg(p_src), 'gi'), p_dst);
    }
    function computeRepPreview() {
      if (!v_rep.src) return [];
      const v_out = [];
      for (const v_row of v_rows.value) {
        if (v_row.status === 'obsolete') continue;
        const v_before = curFix(v_row) ?? v_row.zh_cn ?? '';
        const v_after = v_rep.cs
          ? v_before.split(v_rep.src).join(v_rep.dst)
          : replaceAllCI(v_before, v_rep.src, v_rep.dst);
        if (v_after !== v_before) v_out.push({ display: v_row.display, before: v_before, after: v_after });
      }
      return v_out;
    }
    function applyReplace() {
      const v_hits = computeRepPreview();
      if (!v_hits.length) { toastMsg('没有命中行'); return; }
      const v_undo = new Map();
      const v_keys = new Set();
      for (const v_row of v_rows.value) {
        if (v_row.status === 'obsolete') continue;
        const v_before = curFix(v_row) ?? v_row.zh_cn ?? '';
        const v_after = v_rep.cs
          ? v_before.split(v_rep.src).join(v_rep.dst)
          : replaceAllCI(v_before, v_rep.src, v_rep.dst);
        if (v_after === v_before) continue;
        v_undo.set(v_row.key, curFix(v_row));   // 撤销快照
        v_dirty.set(v_row.key, v_after);         // 与简中相同也存实际值
        v_keys.add(v_row.key);
      }
      v_batchUndo.clear();
      for (const [v_key, v_val] of v_undo) v_batchUndo.set(v_key, v_val);
      v_batchKeys.value = v_keys;
      persistLocal();
      // 记录历史
      v_replaceHistory.value = [
        { src: v_rep.src, dst: v_rep.dst },
        ...v_replaceHistory.value.filter(v_hist => !(v_hist.src === v_rep.src && v_hist.dst === v_rep.dst)),
      ].slice(0, 10);
      localStorage.setItem('syfix_hist', JSON.stringify(v_replaceHistory.value));
      v_modal.replace = false;
      scrollTopTo(0);
      toastMsg(`已替换 ${v_keys.size} 行（未保存，可「还原本次替换」）`);
    }
    function undoBatch() {
      for (const [v_key, v_val] of v_batchUndo) v_dirty.set(v_key, v_val ?? '');
      v_batchUndo.clear();
      v_batchKeys.value = null;
      persistLocal();
      toastMsg('已还原本次批量替换');
    }

    /* ---------------- 导入 / 同步 ---------------- */
    function pickFile(p_role) { v_fileRole = p_role; v_fileEl.value.value = ''; v_fileEl.value.click(); }
    async function onFile(p_event) {
      const v_file = p_event.target.files[0];
      if (!v_file) return;
      const v_content = await v_file.text();
      const v_res = await fetch('/api/import', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ role: v_fileRole, content: v_content, source: v_file.name }),
      });
      const v_data = await v_res.json();
      if (!v_res.ok) { toastMsg(v_data.detail || '导入失败'); return; }
      if (v_fileRole === 'cn') {
        v_hasCn.value = true;
        v_syncSummary.value = v_data;
        v_modal.sync = true;
      } else if (v_fileRole === 'tw') {
        v_hasTw.value = true;
        v_importTitle.value = 'zh-TW 导入完成';
        v_importMsg.value = `共 ${v_data.rows} 行`;
        v_modal.import = true;
      } else if (v_fileRole === 'en') {
        v_importTitle.value = 'en.json 导入完成';
        v_importMsg.value = `共 ${v_data.rows} 行，双击校对时可参考英文原文`;
        v_modal.import = true;
      }
      await reload();
    }
    async function migrateFix(p_pair) {
      const v_res = await fetch('/api/migrate_fix', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ from_key: p_pair.from, to_key: p_pair.to }),
      });
      const v_data = await v_res.json();
      toastMsg(v_data.ok ? `已迁移：${disp(p_pair.to)}` : (v_data.msg || '迁移失败'));
      if (v_data.ok) await reload();
    }

    /* ---------------- 保存 ---------------- */
    async function save() {
      if (!v_dirty.size) { toastMsg('没有未保存的改动'); return; }
      const v_payload = [...v_dirty.entries()].map(([v_key, v_fix]) => ({ key: v_key, fix_cn: v_fix }));
      const v_res = await fetch('/api/save', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ rows: v_payload }),
      });
      if (!v_res.ok) {
        const v_err = await v_res.json().catch(() => ({}));
        toastMsg(v_err.detail || '保存失败');
        return;
      }
      v_dirty.clear();
      v_batchUndo.clear();
      localStorage.removeItem('syfix_dirty');
      await reload();
      toastMsg(`已保存 ${v_payload.length} 行并标记为已核对`);
    }

    /* ---------------- 导出 ---------------- */
    async function doExport() {
      if (v_dirty.size &&
          !confirm(`有 ${v_dirty.size} 条未保存改动，导出不会包含它们。\n点「确定」先自动保存再导出，点「取消」中止。`)) return;
      if (v_dirty.size) { await save(); }
      // Go 桌面版（Wails）：Windows 原生目录选择窗口
      if (window.go && window.go.main && window.go.main.App) {
        try {
          const v_result = await window.go.main.App.ExportWithDialog();
          if (!v_result || v_result.canceled) return;
          if (!v_result.ok) { toastMsg(v_result.msg || '导出失败'); return; }
          v_exportResult.value = v_result;
          v_modal.export = true;
        } catch (v_err) {
          toastMsg('导出失败：' + (v_err.message || v_err));
        }
        return;
      }
      // 现代浏览器：弹出目录选择窗口，免手输路径
      if (window.showDirectoryPicker) {
        let v_dir = null;
        try {
          v_dir = await window.showDirectoryPicker({ mode: 'readwrite' });
        } catch (v_err) {
          if (v_err && v_err.name === 'AbortError') return;   // 用户取消
          v_dir = null;
        }
        if (v_dir) {
          const v_res = await fetch('/api/export_content', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: '{}',
          });
          const v_data = await v_res.json();
          if (!v_res.ok) { toastMsg(v_data.detail || '生成失败'); return; }
          try {
            const v_fileHandle = await v_dir.getFileHandle('zh_CN.json', { create: true });
            const v_writer = await v_fileHandle.createWritable();
            await v_writer.write(v_data.content);
            await v_writer.close();
          } catch (v_err) {
            toastMsg('写入失败：' + (v_err.message || v_err));
            return;
          }
          v_exportResult.value = { ...v_data, out: (v_dir.name || '所选目录') + '\\zh_CN.json' };
          v_modal.export = true;
          return;
        }
      }
      // fallback：手输路径
      const v_dirPath = prompt('导出目录（绝对路径）：', localStorage.getItem('syfix_out') || '');
      if (!v_dirPath) return;
      localStorage.setItem('syfix_out', v_dirPath);
      const v_res = await fetch('/api/export', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ out_dir: v_dirPath }),
      });
      const v_data = await v_res.json();
      if (!v_res.ok) { toastMsg(v_data.detail || '导出失败'); return; }
      v_exportResult.value = v_data;
      v_modal.export = true;
    }

    /* ---------------- 生命周期 ---------------- */

    function onKeydown(p_event) {
      if ((p_event.ctrlKey || p_event.metaKey) && p_event.key === 'f') {
        p_event.preventDefault();
        v_searchEl.value.focus(); v_searchEl.value.select();
      } else if ((p_event.ctrlKey || p_event.metaKey) && p_event.key === 's') {
        p_event.preventDefault(); save();
      } else if (p_event.key === 'Escape') {
        clearFilter();
      }
    }
    function beforeUnload(p_event) {
      if (v_dirty.size) { p_event.preventDefault(); p_event.returnValue = ''; }
    }

    onMounted(async () => {
      // 恢复未保存改动（崩溃/误刷新保护；导入 zh-CN 文件后附着到对应行）
      const v_saved = JSON.parse(localStorage.getItem('syfix_dirty') || '[]');
      for (const [v_key, v_val] of v_saved) v_dirty.set(v_key, v_val);
      if (v_saved.length) toastMsg(`已恢复 ${v_saved.length} 条未保存改动`);

      // 不自动加载：空表引导，由用户导入 zh-CN.json 驱动（版本同步自动合并库中校对成果）
      measure();
      window.addEventListener('keydown', onKeydown);
      window.addEventListener('beforeunload', beforeUnload);
    });
    onBeforeUnmount(() => {
      window.removeEventListener('keydown', onKeydown);
      window.removeEventListener('beforeunload', beforeUnload);
      window.removeEventListener('resize', measure);
    });

    return {
      v_rows, v_dirty, v_batchUndo, v_batchKeys, v_search, v_searchInput,
      v_onlyNew, v_showObsolete, v_onlyPending, v_hasTw, v_hasCn, v_gridCols,
      v_searchCols, colsAny, colsSummary, setCols,
      v_editKey, v_editText, v_rep, v_repPreview, v_replaceHistory, v_histMap,
      v_modal, v_syncSummary, v_importTitle, v_importMsg, v_exportResult, v_toast,
      v_scrollEl, v_searchEl, v_fileEl, v_totalHeight,
      v_filtered, v_pendingCount, v_viewRows,
      onScroll, doSearch, clearFilter,
      rowClass, cellHtml, fixCellHtml, cnTip, curFix, simpColor, simpText,
      startEdit, commitEdit, cancelEdit, onEditBlur, restoreRow,
      openReplace, computeRepPreview, applyReplace, undoBatch,
      pickFile, onFile, migrateFix, disp, save, doExport,
    };
  },
}).mount('#app');
