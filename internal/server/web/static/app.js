// Invenire: o pouco de JavaScript do painel. Tudo funciona sem ele; ele só
// copia tokens e comandos e pede confirmação antes de ações sem volta.
"use strict";

document.addEventListener("click", function (ev) {
  var btn = ev.target.closest("[data-copy]");
  if (!btn) return;
  var el = document.getElementById(btn.getAttribute("data-copy"));
  if (!el || !navigator.clipboard) return;
  navigator.clipboard.writeText(el.textContent).then(function () {
    var old = btn.textContent;
    btn.textContent = "✓";
    setTimeout(function () { btn.textContent = old; }, 1500);
  });
});

document.addEventListener("submit", function (ev) {
  var form = ev.target;
  var text = form.getAttribute("data-confirm");
  if (text && !window.confirm(text)) ev.preventDefault();
});
