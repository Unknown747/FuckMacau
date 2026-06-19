// Highlight active nav link
(function() {
  const path = window.location.pathname;
  document.querySelectorAll('.nav-links a').forEach(a => {
    const href = a.getAttribute('href');
    if (href === path || (href !== '/' && path.startsWith(href))) {
      a.style.color = '#e2e8f0';
      a.style.background = '#334155';
    }
  });
})();
