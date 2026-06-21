// app.js: add/remove scanner rows in the ensemble form.
(function () {
  const tbody = document.querySelector('#scanners-table tbody');
  const addBtn = document.getElementById('add-scanner');
  if (!tbody || !addBtn) return;

  function reindex() {
    tbody.querySelectorAll('tr').forEach((row, i) => {
      row.querySelectorAll('input').forEach(inp => {
        inp.name = inp.name.replace(/_\d+$/, '_' + i);
      });
    });
  }

  addBtn.addEventListener('click', () => {
    const i = tbody.querySelectorAll('tr').length;
    const row = document.createElement('tr');
    row.innerHTML =
      '<td><input name="scanner_name_' + i + '" required></td>' +
      '<td><input name="scanner_provider_' + i + '" required></td>' +
      '<td><input name="scanner_model_' + i + '"></td>' +
      '<td><input name="scanner_weight_' + i + '" type="number" step="0.1" min="0" style="width:5em"></td>' +
      '<td><button type="button" class="remove-row">×</button></td>';
    tbody.appendChild(row);
  });

  tbody.addEventListener('click', (ev) => {
    if (ev.target.classList.contains('remove-row')) {
      ev.target.closest('tr').remove();
      reindex();
    }
  });
})();
