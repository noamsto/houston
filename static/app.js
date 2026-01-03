// houston client-side JS
document.addEventListener('DOMContentLoaded', function() {
    console.log('houston loaded');

    // Update page title with attention count
    function updateTitle() {
        const needsAttention = document.querySelectorAll('[class*="border-red-500"]').length;
        if (needsAttention > 0) {
            document.title = `(${needsAttention}) houston`;
        } else {
            document.title = 'houston';
        }
    }

    // Run on initial load and after htmx swaps
    updateTitle();
    document.body.addEventListener('htmx:afterSwap', updateTitle);

    // Handle SSE reconnection
    document.body.addEventListener('htmx:sseError', function(e) {
        console.log('SSE connection lost, will reconnect...');
    });
});
