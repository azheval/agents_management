// sse-client.js

document.addEventListener('DOMContentLoaded', () => {
    // Apply initial styles to all status badges on page load
    const allBadges = document.querySelectorAll('.task-status-badge');
    allBadges.forEach(badge => {
        updateStatusBadgeClass(badge, badge.textContent.trim());
    });

    initSSE();
});

let eventSource = null;
let retryInterval = 3000; // 3 seconds
const maxRetryInterval = 30000; // 30 seconds
const sseStatusIndicator = document.getElementById('sse-status-indicator');

function initSSE() {
    if (eventSource) {
        eventSource.close();
    }

    eventSource = new EventSource('/events');

    eventSource.onopen = () => {
        if (sseStatusIndicator) {
            sseStatusIndicator.classList.add('hidden');
        }
        retryInterval = 3000;
    };

    eventSource.onmessage = (event) => {
        try {
            const task = JSON.parse(event.data);
            updateTaskInDOM(task);
        } catch (e) {
            console.error('Error parsing SSE message:', e);
        }
    };

    eventSource.onerror = (error) => {
        console.error('SSE error:', error);
        eventSource.close();

        if (sseStatusIndicator) {
            sseStatusIndicator.classList.remove('hidden');
        }

        setTimeout(initSSE, retryInterval);
        retryInterval = Math.min(retryInterval * 2, maxRetryInterval);
    };
}

function updateTaskInDOM(task) {
    if (!task || !task.id) {
        return;
    }
    let taskRow = document.querySelector(`#task-row-${task.id}`);

    if (taskRow) {
        // --- UPDATE EXISTING ROW ---
        const statusBadge = taskRow.querySelector('.task-status-badge');
        if (statusBadge) {
            statusBadge.textContent = task.status;
            updateStatusBadgeClass(statusBadge, task.status);
        }
    } else {
        // --- CREATE NEW ROW ---
        const tbody = document.querySelector('table tbody');
        if (!tbody) {
            return;
        }

        const noTasksRow = tbody.querySelector('td[colspan="6"]');
        if (noTasksRow) {
            noTasksRow.parentElement.remove();
        }

        taskRow = document.createElement('tr');
        taskRow.id = `task-row-${task.id}`;
        taskRow.className = 'hover:bg-gray-50';
        
        const statusBadgeHTML = `<span class="px-2 inline-flex leading-5 font-semibold rounded-full task-status-badge"></span>`;

        taskRow.innerHTML = `
            <td class="px-5 py-4 border-b border-gray-200 text-sm">
                <a href="/task/view?id=${task.id}" class="text-blue-600 hover:underline font-mono">${task.id}</a>
            </td>
            <td class="px-5 py-4 border-b border-gray-200 text-sm font-mono">
                <a href="/?agent_id=${task.agentId}" class="text-blue-600 hover:underline">${task.agentId}</a>
            </td>
            <td class="px-5 py-4 border-b border-gray-200 text-sm">${task.taskType}</td>
            <td class="px-5 py-4 border-b border-gray-200 text-sm">${statusBadgeHTML}</td>
            <td class="px-5 py-4 border-b border-gray-200 text-sm">${new Date(task.createdAt).toLocaleString()}</td>
            <td class="px-5 py-4 border-b border-gray-200 text-sm">
                <a href="/task/view?id=${task.id}" class="text-indigo-600 hover:text-indigo-900">View</a>
            </td>
        `;

        const statusBadge = taskRow.querySelector('.task-status-badge');
        if (statusBadge) {
            statusBadge.textContent = task.status;
            updateStatusBadgeClass(statusBadge, task.status);
        }

        tbody.prepend(taskRow);
    }
}

// Helper function to manage status badge classes
function updateStatusBadgeClass(badgeElement, status) {
    const statusClean = status ? status.trim().toLowerCase() : '';

    // Remove all potential status colors
    badgeElement.classList.remove(
        'bg-gray-200', 'text-gray-800',
        'bg-blue-100', 'text-blue-800',
        'bg-green-100', 'text-green-800',
        'bg-red-100', 'text-red-800'
    );

    switch (statusClean) {
        case 'pending':
            badgeElement.classList.add('bg-gray-200', 'text-gray-800');
            break;
        case 'running':
        case 'assigned':
            badgeElement.classList.add('bg-blue-100', 'text-blue-800');
            break;
        case 'completed':
            badgeElement.classList.add('bg-green-100', 'text-green-800');
            break;
        case 'failed':
        case 'timed_out':
            badgeElement.classList.add('bg-red-100', 'text-red-800');
            break;
        default:
            badgeElement.classList.add('bg-gray-200', 'text-gray-800');
            break;
    }
}