import * as THREE from 'three';
import { OrbitControls } from 'three/addons/controls/OrbitControls.js';

const GRID_SIZE = 20;
const VOXEL_SIZE = 1;
const GAP = 0.05;

let scene, camera, renderer, controls;
let voxelMeshes = [];
let voxelExists = [];
let voxelStress = [];
let loadPoints = new Set();
let supportPoints = new Set();
let raycaster, mouse;
let hoverIndicator;

let currentTool = 'load';
let isOptimizing = false;
let ws = null;
let isConnected = false;

const totalVoxels = GRID_SIZE * GRID_SIZE * GRID_SIZE;

function idx(x, y, z) {
    return x + y * GRID_SIZE + z * GRID_SIZE * GRID_SIZE;
}

function coordFromIdx(i) {
    const x = i % GRID_SIZE;
    const y = Math.floor(i / GRID_SIZE) % GRID_SIZE;
    const z = Math.floor(i / (GRID_SIZE * GRID_SIZE));
    return { x, y, z };
}

function stressToColor(stress) {
    const s = Math.max(0, Math.min(1, stress));
    const r = Math.min(1, s * 2);
    const g = Math.min(1, 2 - s * 2);
    const b = Math.max(0, 0.3 - s * 0.3);
    return new THREE.Color(r, g, b);
}

function voxelWorldPos(x, y, z) {
    const offset = (GRID_SIZE - 1) * (VOXEL_SIZE + GAP) / 2;
    return new THREE.Vector3(
        x * (VOXEL_SIZE + GAP) - offset,
        y * (VOXEL_SIZE + GAP) - offset,
        z * (VOXEL_SIZE + GAP) - offset
    );
}

function init() {
    const container = document.getElementById('canvas-container');

    scene = new THREE.Scene();
    scene.background = new THREE.Color(0x1a1a2e);
    scene.fog = new THREE.Fog(0x1a1a2e, 40, 80);

    camera = new THREE.PerspectiveCamera(60, window.innerWidth / window.innerHeight, 0.1, 1000);
    camera.position.set(25, 20, 30);

    renderer = new THREE.WebGLRenderer({ antialias: true });
    renderer.setSize(window.innerWidth, window.innerHeight);
    renderer.setPixelRatio(Math.min(window.devicePixelRatio, 2));
    renderer.shadowMap.enabled = true;
    container.appendChild(renderer.domElement);

    controls = new OrbitControls(camera, renderer.domElement);
    controls.enableDamping = true;
    controls.dampingFactor = 0.05;
    controls.mouseButtons = {
        LEFT: null,
        MIDDLE: THREE.MOUSE.DOLLY,
        RIGHT: THREE.MOUSE.ROTATE
    };

    const ambientLight = new THREE.AmbientLight(0x404080, 0.6);
    scene.add(ambientLight);

    const dirLight = new THREE.DirectionalLight(0xffffff, 0.8);
    dirLight.position.set(20, 30, 20);
    dirLight.castShadow = true;
    dirLight.shadow.mapSize.width = 2048;
    dirLight.shadow.mapSize.height = 2048;
    dirLight.shadow.camera.near = 0.5;
    dirLight.shadow.camera.far = 100;
    dirLight.shadow.camera.left = -30;
    dirLight.shadow.camera.right = 30;
    dirLight.shadow.camera.top = 30;
    dirLight.shadow.camera.bottom = -30;
    scene.add(dirLight);

    const fillLight = new THREE.DirectionalLight(0x00d9ff, 0.2);
    fillLight.position.set(-20, 10, -20);
    scene.add(fillLight);

    createGroundGrid();
    createVoxelGrid();
    createHoverIndicator();

    raycaster = new THREE.Raycaster();
    mouse = new THREE.Vector2();

    setupEventListeners();
    setupUI();
    connectWebSocket();

    animate();
}

function createGroundGrid() {
    const size = 50;
    const divisions = 50;
    const gridHelper = new THREE.GridHelper(size, divisions, 0x233554, 0x16213e);
    gridHelper.position.y = -GRID_SIZE * (VOXEL_SIZE + GAP) / 2 - 0.5;
    scene.add(gridHelper);

    const groundGeo = new THREE.PlaneGeometry(size, size);
    const groundMat = new THREE.MeshStandardMaterial({
        color: 0x0a192f,
        roughness: 0.9,
        metalness: 0.1
    });
    const ground = new THREE.Mesh(groundGeo, groundMat);
    ground.rotation.x = -Math.PI / 2;
    ground.position.y = -GRID_SIZE * (VOXEL_SIZE + GAP) / 2 - 0.51;
    ground.receiveShadow = true;
    scene.add(ground);
}

function createVoxelGrid() {
    const geometry = new THREE.BoxGeometry(VOXEL_SIZE, VOXEL_SIZE, VOXEL_SIZE);
    const material = new THREE.MeshStandardMaterial({
        color: 0x4a5568,
        roughness: 0.5,
        metalness: 0.3,
        transparent: true,
        opacity: 0.7
    });

    for (let z = 0; z < GRID_SIZE; z++) {
        for (let y = 0; y < GRID_SIZE; y++) {
            for (let x = 0; x < GRID_SIZE; x++) {
                const i = idx(x, y, z);
                const mesh = new THREE.Mesh(geometry, material.clone());
                const pos = voxelWorldPos(x, y, z);
                mesh.position.copy(pos);
                mesh.castShadow = true;
                mesh.receiveShadow = true;
                mesh.userData = { x, y, z, index: i };
                mesh.visible = true;
                scene.add(mesh);
                voxelMeshes.push(mesh);
                voxelExists.push(true);
                voxelStress.push(0);
            }
        }
    }
}

function createHoverIndicator() {
    const geometry = new THREE.BoxGeometry(VOXEL_SIZE * 1.1, VOXEL_SIZE * 1.1, VOXEL_SIZE * 1.1);
    const material = new THREE.MeshBasicMaterial({
        color: 0x00d9ff,
        transparent: true,
        opacity: 0.5,
        wireframe: true
    });
    hoverIndicator = new THREE.Mesh(geometry, material);
    hoverIndicator.visible = false;
    scene.add(hoverIndicator);
}

function updateVoxelVisual(i) {
    const mesh = voxelMeshes[i];
    const exists = voxelExists[i];
    const isLoad = loadPoints.has(i);
    const isSupport = supportPoints.has(i);

    mesh.visible = exists || isLoad || isSupport;

    if (!mesh.visible) return;

    if (isLoad) {
        mesh.material.color.setHex(0xff6b6b);
        mesh.material.opacity = 1;
        mesh.material.emissive = new THREE.Color(0xff3333);
        mesh.material.emissiveIntensity = 0.5;
        mesh.scale.setScalar(1.15);
    } else if (isSupport) {
        mesh.material.color.setHex(0x00d9ff);
        mesh.material.opacity = 1;
        mesh.material.emissive = new THREE.Color(0x0099cc);
        mesh.material.emissiveIntensity = 0.5;
        mesh.scale.setScalar(1.15);
    } else if (exists) {
        const stress = voxelStress[i];
        mesh.material.color.copy(stressToColor(stress));
        mesh.material.opacity = 0.85;
        mesh.material.emissive = new THREE.Color(0x000000);
        mesh.material.emissiveIntensity = 0;
        mesh.scale.setScalar(1);
    }
}

function updateAllVoxelsVisual() {
    for (let i = 0; i < totalVoxels; i++) {
        updateVoxelVisual(i);
    }
}

function setupEventListeners() {
    const canvas = renderer.domElement;

    canvas.addEventListener('mousemove', onMouseMove);
    canvas.addEventListener('click', onMouseClick);
    canvas.addEventListener('contextmenu', (e) => e.preventDefault());
    window.addEventListener('resize', onWindowResize);
}

function onMouseMove(event) {
    mouse.x = (event.clientX / window.innerWidth) * 2 - 1;
    mouse.y = -(event.clientY / window.innerHeight) * 2 + 1;

    raycaster.setFromCamera(mouse, camera);
    const intersects = raycaster.intersectObjects(voxelMeshes.filter(m => m.visible));

    if (intersects.length > 0 && currentTool !== 'none') {
        const hit = intersects[0];
        hoverIndicator.position.copy(hit.object.position);
        hoverIndicator.visible = true;
        if (currentTool === 'load') {
            hoverIndicator.material.color.setHex(0xff6b6b);
        } else if (currentTool === 'support') {
            hoverIndicator.material.color.setHex(0x00d9ff);
        }
    } else {
        hoverIndicator.visible = false;
    }
}

function onMouseClick(event) {
    if (event.button !== 0 || currentTool === 'none') return;

    mouse.x = (event.clientX / window.innerWidth) * 2 - 1;
    mouse.y = -(event.clientY / window.innerHeight) * 2 + 1;

    raycaster.setFromCamera(mouse, camera);
    const intersects = raycaster.intersectObjects(voxelMeshes.filter(m => m.visible));

    if (intersects.length > 0) {
        const hit = intersects[0];
        const { x, y, z, index: i } = hit.object.userData;

        if (currentTool === 'load') {
            if (loadPoints.has(i)) {
                loadPoints.delete(i);
                sendMessage('removeLoad', { x, y, z });
            } else {
                supportPoints.delete(i);
                loadPoints.add(i);
                sendMessage('setLoad', { x, y, z });
            }
        } else if (currentTool === 'support') {
            if (supportPoints.has(i)) {
                supportPoints.delete(i);
                sendMessage('removeSupport', { x, y, z });
            } else {
                loadPoints.delete(i);
                supportPoints.add(i);
                sendMessage('setSupport', { x, y, z });
            }
        }

        updateVoxelVisual(i);
        updateStatusUI();
    }
}

function onWindowResize() {
    camera.aspect = window.innerWidth / window.innerHeight;
    camera.updateProjectionMatrix();
    renderer.setSize(window.innerWidth, window.innerHeight);
}

function setupUI() {
    document.querySelectorAll('.tool-btn').forEach(btn => {
        btn.addEventListener('click', () => {
            document.querySelectorAll('.tool-btn').forEach(b => b.classList.remove('active'));
            btn.classList.add('active');
            currentTool = btn.dataset.tool;
            hoverIndicator.visible = false;
        });
    });

    const volumeSlider = document.getElementById('volume-slider');
    const volumeValue = document.getElementById('volume-value');
    volumeSlider.addEventListener('input', () => {
        const val = parseInt(volumeSlider.value);
        volumeValue.textContent = val;
        sendMessage('setConfig', { targetVolume: val / 100 });
    });

    document.getElementById('start-btn').addEventListener('click', () => {
        if (!isConnected || loadPoints.size === 0 || supportPoints.size === 0) {
            alert('请先连接服务器并放置至少一个受力点和一个支撑点！');
            return;
        }
        if (!isOptimizing) {
            isOptimizing = true;
            document.getElementById('start-btn').textContent = '⏸ 优化中...';
            document.getElementById('start-btn').disabled = true;
            sendMessage('iterateAll', {});
        }
    });

    document.getElementById('step-btn').addEventListener('click', () => {
        if (!isConnected || loadPoints.size === 0 || supportPoints.size === 0) {
            alert('请先连接服务器并放置至少一个受力点和一个支撑点！');
            return;
        }
        sendMessage('iterate', {});
    });

    document.getElementById('reset-btn').addEventListener('click', () => {
        isOptimizing = false;
        document.getElementById('start-btn').textContent = '▶ 开始优化';
        document.getElementById('start-btn').disabled = false;
        loadPoints.clear();
        supportPoints.clear();
        for (let i = 0; i < totalVoxels; i++) {
            voxelExists[i] = true;
            voxelStress[i] = 0;
        }
        updateAllVoxelsVisual();
        updateStatusUI();
        sendMessage('reset', {});
    });
}

function updateStatusUI(data) {
    document.getElementById('conn-status').textContent = isConnected ? '已连接' : '未连接';
    document.getElementById('conn-status').style.color = isConnected ? '#00ff88' : '#ff6b6b';
    document.getElementById('load-count').textContent = loadPoints.size;
    document.getElementById('support-count').textContent = supportPoints.size;

    if (data) {
        document.getElementById('iter-count').textContent = data.iteration || 0;
        const volPercent = Math.round((data.volumeRatio || 1) * 100);
        document.getElementById('current-vol').textContent = volPercent + '%';
        const targetVol = data.targetVolume || 0.4;
        const currentVol = data.volumeRatio || 1;
        const progress = currentVol > targetVol
            ? Math.min(100, ((1 - currentVol) / (1 - targetVol)) * 100)
            : 100;
        document.getElementById('progress-fill').style.width = progress + '%';

        if (data.isConverged) {
            isOptimizing = false;
            document.getElementById('start-btn').textContent = '✓ 完成';
            document.getElementById('start-btn').disabled = false;
        }
    }
}

function connectWebSocket() {
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const wsUrl = `${protocol}//${window.location.host}/ws`;

    ws = new WebSocket(wsUrl);
    ws.binaryType = 'arraybuffer';

    ws.onopen = () => {
        isConnected = true;
        updateStatusUI();
        console.log('WebSocket connected');
    };

    ws.onclose = () => {
        isConnected = false;
        isOptimizing = false;
        document.getElementById('start-btn').textContent = '▶ 开始优化';
        document.getElementById('start-btn').disabled = false;
        updateStatusUI();
        console.log('WebSocket disconnected, reconnecting in 3s...');
        setTimeout(connectWebSocket, 3000);
    };

    ws.onerror = (err) => {
        console.error('WebSocket error:', err);
    };

    ws.onmessage = async (event) => {
        if (event.data instanceof ArrayBuffer) {
            await handleBinaryMessage(event.data);
        } else {
            handleTextMessage(event.data);
        }
    };
}

async function decompressGzip(buffer) {
    const ds = new DecompressionStream('gzip');
    const blob = new Blob([buffer]);
    const decompressedStream = blob.stream().pipeThrough(ds);
    const reader = decompressedStream.getReader();
    const chunks = [];
    let result;
    do {
        result = await reader.read();
        if (result.value) chunks.push(result.value);
    } while (!result.done);

    const totalLength = chunks.reduce((sum, c) => sum + c.length, 0);
    const decompressed = new Uint8Array(totalLength);
    let offset = 0;
    for (const chunk of chunks) {
        decompressed.set(chunk, offset);
        offset += chunk.length;
    }
    return decompressed;
}

async function handleBinaryMessage(buffer) {
    const data = new Uint8Array(buffer);
    const msgType = data[0];
    const payload = new Uint8Array(buffer, 1);

    if (msgType === 1) {
        await decodeFullGrid(payload.buffer.slice(payload.byteOffset, payload.byteOffset + payload.byteLength));
    } else if (msgType === 2) {
        await decodeVoxelChanges(payload.buffer.slice(payload.byteOffset, payload.byteOffset + payload.byteLength));
    }
}

async function decodeFullGrid(buffer) {
    const data = await decompressGzip(buffer);
    const view = new DataView(data.buffer);
    let offset = 0;

    const sizeX = view.getUint32(offset, true); offset += 4;
    const sizeY = view.getUint32(offset, true); offset += 4;
    const sizeZ = view.getUint32(offset, true); offset += 4;

    const total = sizeX * sizeY * sizeZ;
    const bitCount = Math.ceil(total / 8);

    for (let i = 0; i < total; i++) {
        const byteIdx = Math.floor(i / 8);
        const bitIdx = i % 8;
        voxelExists[i] = ((data[offset + byteIdx] >> bitIdx) & 1) === 1;
    }
    offset += bitCount;

    for (let i = 0; i < total; i++) {
        voxelStress[i] = data[offset + i] / 255.0;
    }
    offset += total;

    loadPoints.clear();
    const loadCount = view.getUint32(offset, true); offset += 4;
    for (let i = 0; i < loadCount; i++) {
        const idx = view.getUint16(offset, true); offset += 2;
        loadPoints.add(idx);
    }

    supportPoints.clear();
    const supportCount = view.getUint32(offset, true); offset += 4;
    for (let i = 0; i < supportCount; i++) {
        const idx = view.getUint16(offset, true); offset += 2;
        supportPoints.add(idx);
    }

    updateAllVoxelsVisual();
    updateStatusUI();
}

async function decodeVoxelChanges(buffer) {
    const data = await decompressGzip(buffer);
    const view = new DataView(data.buffer);
    let offset = 0;

    const count = view.getUint32(offset, true); offset += 4;

    for (let i = 0; i < count; i++) {
        const index = view.getUint16(offset, true); offset += 2;
        offset += 6;
        const action = data[offset]; offset += 1;
        const stressByte = data[offset]; offset += 1;

        if (action === 1) {
            voxelExists[index] = true;
        } else if (action === 2) {
            voxelExists[index] = false;
        }
        voxelStress[index] = stressByte / 255.0;
        updateVoxelVisual(index);
    }
}

function handleTextMessage(data) {
    try {
        const msg = JSON.parse(data);
        if (msg.type === 'state' || msg.type === 'iteration' || msg.type === 'done') {
            updateStatusUI(msg.payload);
        }
    } catch (e) {
        console.error('JSON parse error:', e);
    }
}

function sendMessage(type, payload) {
    if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type, payload }));
    }
}

function animate() {
    requestAnimationFrame(animate);
    controls.update();

    const time = Date.now() * 0.001;
    loadPoints.forEach(i => {
        const mesh = voxelMeshes[i];
        if (mesh) {
            const { x, y, z } = coordFromIdx(i);
            const pos = voxelWorldPos(x, y, z);
            mesh.position.set(pos.x, pos.y + Math.sin(time * 3) * 0.15, pos.z);
        }
    });

    supportPoints.forEach(i => {
        const mesh = voxelMeshes[i];
        if (mesh) {
            const { x, y, z } = coordFromIdx(i);
            const pos = voxelWorldPos(x, y, z);
            const pulse = 1 + Math.sin(time * 2) * 0.05;
            mesh.scale.setScalar(1.15 * pulse);
        }
    });

    renderer.render(scene, camera);
}

init();
