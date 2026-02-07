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

// OpenCode: Abort session
function abortOpenCodeSession(serverURL, sessionID) {
    if (!confirm('Abort this OpenCode session?')) return;
    
    const encodedServer = encodeURIComponent(serverURL);
    fetch(`/opencode/session/${encodedServer}/${sessionID}/abort`, {
        method: 'POST'
    })
    .then(response => {
        if (response.ok) {
            console.log('OpenCode session aborted');
            // Refresh the sessions list
            if (window.htmx) {
                htmx.trigger(document.body, 'refresh');
            }
        } else {
            console.error('Failed to abort OpenCode session');
            alert('Failed to abort session');
        }
    })
    .catch(err => {
        console.error('OpenCode abort error:', err);
        alert('Error aborting session');
    });
}


