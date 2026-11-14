// Shows the feed address as the visitor sees it (also on a LAN copy or a USB stick), and adds copy buttons.
// Nothing is loaded from anywhere; no cookies, no storage.
(function () {
  "use strict";
  var configured = document.documentElement.getAttribute("data-url");
  if (!configured) {
    var here = new URL("feed.json", window.location.href);
    here.search = ""; here.hash = "";
    document.querySelectorAll(".feed-url").forEach(function (el) { el.textContent = here.href; });
  }
  document.querySelectorAll("button.copy").forEach(function (button) {
    var target = document.getElementById(button.getAttribute("data-copy"));
    if (!target) return;
    button.hidden = false;
    button.addEventListener("click", function () {
      var text = target.textContent;
      var done = function () {
        button.textContent = "Copied";
        setTimeout(function () { button.textContent = "Copy"; }, 1500);
      };
      if (navigator.clipboard && window.isSecureContext) {
        navigator.clipboard.writeText(text).then(done, function () { select(target); });
      } else {
        select(target); // plain http (LAN): let the visitor press Ctrl+C
      }
    });
  });
  // Song pages: filter the list as you type (without JavaScript, the browser's Find works as well).
  var filter = document.getElementById("filter");
  var list = document.getElementById("songs");
  if (filter && list) {
    var items = Array.prototype.slice.call(list.children);
    var texts = items.map(function (li) { return li.textContent.toLowerCase(); });
    var count = document.getElementById("filter-count");
    var timer = null;
    filter.parentNode.hidden = false;
    var apply = function () {
      var words = filter.value.toLowerCase().split(/\s+/).filter(Boolean);
      var shown = 0;
      for (var i = 0; i < items.length; i++) {
        var hit = words.every(function (w) { return texts[i].indexOf(w) !== -1; });
        items[i].hidden = !hit;
        if (hit) shown++;
      }
      count.textContent = words.length ? shown + " of " + items.length + " songs" : "";
    };
    filter.addEventListener("input", function () { clearTimeout(timer); timer = setTimeout(apply, 120); });
  }
  function select(el) {
    var range = document.createRange();
    range.selectNodeContents(el);
    var sel = window.getSelection();
    sel.removeAllRanges();
    sel.addRange(range);
  }
})();
