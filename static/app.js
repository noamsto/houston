// tmux-dashboard client-side JS
document.addEventListener('DOMContentLoaded', function() {
    console.log('tmux-dashboard loaded');

    // Update page title with attention count
    function updateTitle() {
        const needsAttention = document.querySelectorAll('[class*="border-red-500"]').length;
        if (needsAttention > 0) {
            document.title = `(${needsAttention}) tmux-dashboard`;
        } else {
            document.title = 'tmux-dashboard';
        }
    }

    // Run on initial load and after htmx swaps
    updateTitle();
    document.body.addEventListener('htmx:afterSwap', updateTitle);

    // Handle SSE reconnection
    document.body.addEventListener('htmx:sseError', function(e) {
        console.log('SSE connection lost, will reconnect...');
    });

    // Auto-scroll output on new content
    document.body.addEventListener('htmx:afterSwap', function(e) {
        if (e.target.id === 'output') {
            const container = document.getElementById('output-container');
            if (container) {
                // Only auto-scroll if already near bottom
                const isNearBottom = container.scrollHeight - container.scrollTop - container.clientHeight < 100;
                if (isNearBottom) {
                    container.scrollTop = container.scrollHeight;
                }
            }
        }
    });
});
