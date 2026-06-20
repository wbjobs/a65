import * as THREE from 'three';
import { OrbitControls } from 'three/addons/controls/OrbitControls.js';

const GRID_SIZE = 20;
const VOXEL_SIZE = 1;
const GAP = 0.05;
const TOTAL_VOXELS = GRID_SIZE * GRID_SIZE * GRID_SIZE;

const FATIGUE_WARNING = 0.4;
const FATIGUE_LIMIT = 0.7;

let scene, camera, renderer, controls;
let voxelMesh;
let dummyMatrix;
let voxelExists = new Uint8Array(TOTAL_VOXELS);
let voxelStress = new Float32Array(TOTAL_VOXELS);
let voxelFatigue = new Float32Array(TOTAL_VOXELS);
let loadPoints = new Set();
let supportPoints = new Set();
let raycaster, mouse;
let hoverIndicator;

let instancesDirty = true;
let colorsDirty = true;

let currentTool = 'load';
let isOptimizing = false;
let isDynamicRunning = false;
let ws = null;
let isConnected = false;

let currentTimeStep = 0;
let currentTime = 0;
let currentLoadValue = 1.0;

function idx(x, y, z) {
    return x + y * GRID_SIZE + z * GRID_SIZE * GRID_SIZE;
}

function coordFromIdx(i) {
    const x = i % GRID_SIZE;
    const y = Math.floor(i / GRID_SIZE) % GRID_SIZE;
    const z = Math.floor(i / (GRID_SIZE * GRID_SIZE));
    return { x, y, z };
}

function voxelToColor(stress, fatigue, isLoad, isSupport) {
    if (isLoad) return new THREE.Color(0xff6b6b);
    if (isSupport) return new THREE.Color(0x00d9ff);

    if (fatigue >= FATIGUE_LIMIT) {
        const t = (fatigue - FATIGUE_LIMIT) / (1.0 - FATIGUE_LIMIT);
        return new THREE.Color().lerpColors(
            new THREE.Color(0xff6b6b),
            new THREE.Color(0x8b0000),
            Math.min(1, t)
        );
    } else if (fatigue >= FATIGUE_WARNING) {
        const t = (fatigue - FATIGUE_WARNING) / (FATIGUE_LIMIT - FATIGUE_WARNING);
        return new THREE.Color().lerpColors(
            new THREE.Color(0xff9f43),
            new THREE.Color(0xff6b6b),
            t
        );
    }

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

async function decodeMessagePack(buffer) {
    const { decode } = await import('https://esm.sh/@msgpack/msgpack@3.0.0-beta2');
    return decode(buffer);
}

async function decompressGzip(buffer) {
    try {
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
    } catch (e) {
        console.warn('Gzip decompression failed, trying raw:', e);
        return new Uint8Array(buffer);
    }
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
    scene.add(dirLight);

    const fillLight = new THREE.DirectionalLight(0x00d9ff, 0.2);
    fillLight.position.set(-20, 10, -20);
    scene.add(fillLight);

    dummyMatrix = new THREE.Matrix4();

    createGroundGrid();
    createInstancedVoxels();
    createHoverIndicator();

    voxelExists.fill(1);

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

function createInstancedVoxels() {
    const geometry = new THREE.BoxGeometry(VOXEL_SIZE, VOXEL_SIZE, VOXEL_SIZE);

    const material = new THREE.MeshStandardMaterial({
        roughness: 0.5,
        metalness: 0.3,
        vertexColors: false
    });

    voxelMesh = new THREE.InstancedMesh(geometry, material, TOTAL_VOXELS);
    voxelMesh.castShadow = true;
    voxelMesh.receiveShadow = true;
    voxelMesh.instanceMatrix.setUsage(THREE.DynamicDrawUsage);

    voxelMesh.userData.voxelIndices = new Array(TOTAL_VOXELS);

    for (let i = 0; i < TOTAL_VOXELS; i++) {
        const { x, y, z } = coordFromIdx(i);
        const pos = voxelWorldPos(x, y, z);
        dummyMatrix.makeTranslation(pos.x, pos.y, pos.z);
        voxelMesh.setMatrixAt(i, dummyMatrix);
        voxelMesh.userData.voxelIndices[i] = i;
    }

    voxelMesh.instanceColor = new THREE.InstancedBufferAttribute(new Float32Array(TOTAL_VOXELS * 3), 3);
    voxelMesh.instanceColor.setUsage(THREE.DynamicDrawUsage);

    const defaultColor = new THREE.Color(0x4a5568);
    for (let i = 0; i < TOTAL_VOXELS; i++) {
        voxelMesh.setColorAt(i, defaultColor);
    }

    voxelMesh.instanceMatrix.needsUpdate = true;
    voxelMesh.instanceColor.needsUpdate = true;

    scene.add(voxelMesh);
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

function updateVoxelInstance(i) {
    const exists = voxelExists[i];
    const isLoad = loadPoints.has(i);
    const isSupport = supportPoints.has(i);

    if (exists || isLoad || isSupport) {
        const { x, y, z } = coordFromIdx(i);
        const pos = voxelWorldPos(x, y, z);
        const scale = isLoad || isSupport ? 1.15 : 1.0;
        dummyMatrix.compose(
            new THREE.Vector3(pos.x, pos.y, pos.z),
            new THREE.Quaternion(),
            new THREE.Vector3(scale, scale, scale)
        );
        voxelMesh.setMatrixAt(i, dummyMatrix);

        const color = voxelToColor(voxelStress[i], voxelFatigue[i], isLoad, isSupport);
        voxelMesh.setColorAt(i, color);
    } else {
        dummyMatrix.makeScale(0, 0, 0);
        voxelMesh.setMatrixAt(i, dummyMatrix);
    }

    instancesDirty = true;
}

function updateAllInstances() {
    const hiddenMatrix = new THREE.Matrix4().makeScale(0, 0, 0);

    for (let i = 0; i < TOTAL_VOXELS; i++) {
        const exists = voxelExists[i];
        const isLoad = loadPoints.has(i);
        const isSupport = supportPoints.has(i);

        if (exists || isLoad || isSupport) {
            const { x, y, z } = coordFromIdx(i);
            const pos = voxelWorldPos(x, y, z);
            const scale = isLoad || isSupport ? 1.15 : 1.0;
            dummyMatrix.compose(
                new THREE.Vector3(pos.x, pos.y, pos.z),
                new THREE.Quaternion(),
                new THREE.Vector3(scale, scale, scale)
            );
            voxelMesh.setMatrixAt(i, dummyMatrix);

            const color = voxelToColor(voxelStress[i], voxelFatigue[i], isLoad, isSupport);
            voxelMesh.setColorAt(i, color);
        } else {
            voxelMesh.setMatrixAt(i, hiddenMatrix);
        }
    }

    instancesDirty = true;
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
    const intersects = raycaster.intersectObject(voxelMesh);

    if (intersects.length > 0 && currentTool !== 'none') {
        const hit = intersects[0];
        const instanceId = hit.instanceId;
        if (instanceId !== undefined) {
            const { x, y, z } = coordFromIdx(instanceId);
            const pos = voxelWorldPos(x, y, z);
            hoverIndicator.position.copy(pos);
            hoverIndicator.visible = true;
            hoverIndicator.material.color.setHex(
                currentTool === 'load' ? 0xff6b6b : 0x00d9ff
            );
            return;
        }
    }
    hoverIndicator.visible = false;
}

function onMouseClick(event) {
    if (event.button !== 0 || currentTool === 'none') return;

    mouse.x = (event.clientX / window.innerWidth) * 2 - 1;
    mouse.y = -(event.clientY / window.innerHeight) * 2 + 1;

    raycaster.setFromCamera(mouse, camera);
    const intersects = raycaster.intersectObject(voxelMesh);

    if (intersects.length > 0) {
        const hit = intersects[0];
        const instanceId = hit.instanceId;
        if (instanceId !== undefined) {
            const { x, y, z } = coordFromIdx(instanceId);
            const i = instanceId;

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

            updateVoxelInstance(i);
            updateStatusUI();
        }
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

    const freqSlider = document.getElementById('freq-slider');
    const freqValue = document.getElementById('freq-value');
    freqSlider.addEventListener('input', () => {
        const val = parseFloat(freqSlider.value);
        freqValue.textContent = val.toFixed(1);
        sendDynamicParams();
    });

    const ampSlider = document.getElementById('amp-slider');
    const ampValue = document.getElementById('amp-value');
    ampSlider.addEventListener('input', () => {
        const val = parseFloat(ampSlider.value);
        ampValue.textContent = val.toFixed(1);
        sendDynamicParams();
    });

    document.getElementById('start-btn').addEventListener('click', () => {
        if (!isConnected || loadPoints.size === 0 || supportPoints.size === 0) {
            alert('请先连接服务器并放置至少一个受力点和一个支撑点！');
            return;
        }
        if (!isOptimizing && !isDynamicRunning) {
            isOptimizing = true;
            document.getElementById('start-btn').textContent = '⏸ 优化中...';
            document.getElementById('start-btn').disabled = true;
            sendMessage('iterateAll', {});
        }
    });

    document.getElementById('dynamic-btn').addEventListener('click', () => {
        if (!isConnected || loadPoints.size === 0 || supportPoints.size === 0) {
            alert('请先连接服务器并放置至少一个受力点和一个支撑点！');
            return;
        }
        if (!isDynamicRunning) {
            sendDynamicParams();
            setTimeout(() => {
                isDynamicRunning = true;
                isOptimizing = true;
                document.getElementById('dynamic-btn').classList.add('active');
                document.getElementById('dynamic-btn').textContent = '⏸ 动态优化中...';
                document.getElementById('start-btn').disabled = true;
                sendMessage('startDynamic', {});
            }, 100);
        } else {
            isDynamicRunning = false;
            isOptimizing = false;
            document.getElementById('dynamic-btn').classList.remove('active');
            document.getElementById('dynamic-btn').textContent = '↻ 动态优化';
            document.getElementById('start-btn').disabled = false;
            sendMessage('stopDynamic', {});
        }
    });

    document.getElementById('step-btn').addEventListener('click', () => {
        if (!isConnected || loadPoints.size === 0 || supportPoints.size === 0) {
            alert('请先连接服务器并放置至少一个受力点和一个支撑点！');
            return;
        }
        sendMessage('iterate', {});
    });

    document.getElementById('step-time-btn').addEventListener('click', () => {
        if (!isConnected || loadPoints.size === 0 || supportPoints.size === 0) {
            alert('请先连接服务器并放置至少一个受力点和一个支撑点！');
            return;
        }
        sendMessage('stepTime', {});
    });

    document.getElementById('reset-btn').addEventListener('click', () => {
        isOptimizing = false;
        isDynamicRunning = false;
        document.getElementById('start-btn').textContent = '▶ 静态优化';
        document.getElementById('start-btn').disabled = false;
        document.getElementById('dynamic-btn').classList.remove('active');
        document.getElementById('dynamic-btn').textContent = '↻ 动态优化';
        loadPoints.clear();
        supportPoints.clear();
        voxelExists.fill(1);
        voxelStress.fill(0);
        voxelFatigue.fill(0);
        currentTimeStep = 0;
        currentTime = 0;
        updateAllInstances();
        updateStatusUI();
        sendMessage('reset', {});
    });
}

function sendDynamicParams() {
    const freq = parseFloat(document.getElementById('freq-slider').value);
    const amp = parseFloat(document.getElementById('amp-slider').value);
    sendMessage('setDynamicParams', {
        frequency: freq,
        amplitude: amp,
        enabled: true
    });
}

function updateStatusUI(data) {
    document.getElementById('conn-status').textContent = isConnected ? '已连接' : '未连接';
    document.getElementById('conn-status').style.color = isConnected ? '#00ff88' : '#ff6b6b';
    document.getElementById('load-count').textContent = loadPoints.size;
    document.getElementById('support-count').textContent = supportPoints.size;
    document.getElementById('time-step').textContent = currentTimeStep;

    if (data) {
        const iter = data.i !== undefined ? data.i : (data.iteration || 0);
        document.getElementById('iter-count').textContent = iter;

        const volPercent = Math.round((data.vr !== undefined ? data.vr : (data.volumeRatio || 1)) * 100);
        document.getElementById('current-vol').textContent = volPercent + '%';

        const targetVol = data.tv !== undefined ? data.tv : (data.targetVolume || 0.4);
        const currentVol = data.vr !== undefined ? data.vr : (data.volumeRatio || 1);
        const progress = currentVol > targetVol
            ? Math.min(100, ((1 - currentVol) / (1 - targetVol)) * 100)
            : 100;
        document.getElementById('progress-fill').style.width = progress + '%';

        if (data.ts !== undefined) {
            currentTimeStep = data.ts;
            document.getElementById('time-step').textContent = currentTimeStep;
        }
        if (data.t !== undefined) {
            currentTime = data.t;
        }
        if (data.clv !== undefined) {
            currentLoadValue = data.clv;
            const loadPercent = ((currentLoadValue + 2) / 4) * 100;
            document.getElementById('load-fill').style.width = Math.max(0, Math.min(100, loadPercent)) + '%';
        }

        const fwc = data.fwc !== undefined ? data.fwc : (data.fatigueWarningCount || 0);
        const warnEl = document.getElementById('fatigue-warn');
        warnEl.textContent = fwc;
        warnEl.className = 'value' + (fwc > 0 ? ' warning' : '');

        const fcc = data.fcc !== undefined ? data.fcc : (data.fatigueCriticalCount || 0);
        const dangerEl = document.getElementById('fatigue-danger');
        dangerEl.textContent = fcc;
        dangerEl.className = 'value' + (fcc > 0 ? ' danger' : '');

        const mfd = data.mfd !== undefined ? data.mfd : (data.maxFatigueDamage || 0);
        document.getElementById('max-fatigue').textContent = Math.round(mfd * 100) + '%';

        const udl = data.udl !== undefined ? data.udl : (data.useDynamicLoad || false);
        if (udl !== undefined && !isDynamicRunning && !isOptimizing) {
        }

        if (data.ic !== undefined ? data.ic : data.isConverged) {
            isOptimizing = false;
            isDynamicRunning = false;
            document.getElementById('start-btn').textContent = '✓ 完成';
            document.getElementById('start-btn').disabled = false;
            document.getElementById('dynamic-btn').classList.remove('active');
            document.getElementById('dynamic-btn').textContent = '↻ 动态优化';
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
        isDynamicRunning = false;
        document.getElementById('start-btn').textContent = '▶ 静态优化';
        document.getElementById('start-btn').disabled = false;
        document.getElementById('dynamic-btn').classList.remove('active');
        document.getElementById('dynamic-btn').textContent = '↻ 动态优化';
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

async function handleBinaryMessage(buffer) {
    const data = new Uint8Array(buffer);
    const msgType = data[0];
    const payload = new Uint8Array(buffer, 1);
    const payloadBuf = payload.buffer.slice(payload.byteOffset, payload.byteOffset + payload.byteLength);

    if (msgType === 1) {
        await decodeFullGrid(payloadBuf);
    } else if (msgType === 2) {
        await decodeVoxelChanges(payloadBuf);
    } else if (msgType === 3 || msgType === 4 || msgType === 5) {
        try {
            const decompressed = await decompressGzip(payload);
            const state = await decodeMessagePack(decompressed);
            updateStatusUI(state);
        } catch (e) {
            console.error('MsgPack decode error:', e);
        }
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
        voxelExists[i] = ((data[offset + byteIdx] >> bitIdx) & 1);
    }
    offset += bitCount;

    for (let i = 0; i < total; i++) {
        voxelStress[i] = data[offset + i] / 255.0;
    }
    offset += total;

    for (let i = 0; i < total; i++) {
        voxelFatigue[i] = data[offset + i] / 255.0;
    }
    offset += total;

    loadPoints.clear();
    const loadCount = view.getUint32(offset, true); offset += 4;
    const supportCount = view.getUint32(offset, true); offset += 4;

    for (let i = 0; i < loadCount; i++) {
        const idx = view.getUint16(offset, true); offset += 2;
        loadPoints.add(idx);
    }

    for (let i = 0; i < supportCount; i++) {
        const idx = view.getUint16(offset, true); offset += 2;
        supportPoints.add(idx);
    }

    updateAllInstances();
    updateStatusUI();
}

async function decodeVoxelChanges(buffer) {
    try {
        const decompressed = await decompressGzip(buffer);
        const changes = await decodeMessagePack(decompressed);

        if (Array.isArray(changes)) {
            for (const ch of changes) {
                const index = ch.i !== undefined ? ch.i : ch.index;
                const action = ch.a !== undefined ? ch.a : ch.action;
                const stress = ((ch.s !== undefined ? ch.s : ch.stress) || 0) / 255.0;
                const fatigue = ((ch.f !== undefined ? ch.f : ch.fatigueDamage) || 0) / 255.0;

                if (action === 1) {
                    voxelExists[index] = 1;
                } else if (action === 2) {
                    voxelExists[index] = 0;
                }
                voxelStress[index] = stress;
                voxelFatigue[index] = fatigue;
                updateVoxelInstance(index);
            }
        }
    } catch (e) {
        console.error('Decode changes error:', e);
    }
}

function handleTextMessage(data) {
    try {
        const msg = JSON.parse(data);
        if (msg.type === 'done') {
            updateStatusUI({
                iteration: msg.payload.iteration,
                volumeRatio: msg.payload.volumeRatio,
                isConverged: true
            });
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

    if (instancesDirty && voxelMesh) {
        voxelMesh.instanceMatrix.needsUpdate = true;
        voxelMesh.instanceColor.needsUpdate = true;
        instancesDirty = false;
    }

    const time = Date.now() * 0.001;
    loadPoints.forEach(i => {
        if (voxelExists[i] || loadPoints.has(i)) {
            const { x, y, z } = coordFromIdx(i);
            const pos = voxelWorldPos(x, y, z);
            const loadOffset = isDynamicRunning ? Math.sin(currentTime * Math.PI * 2 * (parseFloat(document.getElementById('freq-slider').value) || 1)) * 0.2 : Math.sin(time * 3) * 0.15;
            dummyMatrix.compose(
                new THREE.Vector3(pos.x, pos.y + loadOffset, pos.z),
                new THREE.Quaternion(),
                new THREE.Vector3(1.15, 1.15, 1.15)
            );
            voxelMesh.setMatrixAt(i, dummyMatrix);
            instancesDirty = true;
        }
    });

    supportPoints.forEach(i => {
        if (voxelExists[i] || supportPoints.has(i)) {
            const pulse = 1 + Math.sin(time * 2) * 0.05;
            const { x, y, z } = coordFromIdx(i);
            const pos = voxelWorldPos(x, y, z);
            dummyMatrix.compose(
                new THREE.Vector3(pos.x, pos.y, pos.z),
                new THREE.Quaternion(),
                new THREE.Vector3(1.15 * pulse, 1.15 * pulse, 1.15 * pulse)
            );
            voxelMesh.setMatrixAt(i, dummyMatrix);
            instancesDirty = true;
        }
    });

    renderer.render(scene, camera);
}

init();

window.__test = {
    addLoadPoint: (x, y, z) => {
        sendMessage('setLoad', { x, y, z });
        loadPoints.add(idx(x, y, z));
        console.log('Load point added:', x, y, z);
    },
    addSupportPoint: (x, y, z) => {
        sendMessage('setSupport', { x, y, z });
        supportPoints.add(idx(x, y, z));
        console.log('Support point added:', x, y, z);
    },
    setDynamicParams: (freq, amp) => {
        sendMessage('setDynamicParams', { frequency: freq, amplitude: amp, enabled: true });
        console.log('Dynamic params set:', freq, amp);
    },
    startDynamic: () => {
        isDynamicRunning = true;
        isOptimizing = true;
        document.getElementById('dynamic-btn').classList.add('active');
        document.getElementById('dynamic-btn').textContent = '⏸ 动态优化中...';
        document.getElementById('start-btn').disabled = true;
        sendMessage('startDynamic', {});
        console.log('Dynamic optimization started');
    },
    stopDynamic: () => {
        isDynamicRunning = false;
        isOptimizing = false;
        document.getElementById('dynamic-btn').classList.remove('active');
        document.getElementById('dynamic-btn').textContent = '↻ 动态优化';
        document.getElementById('start-btn').disabled = false;
        sendMessage('stopDynamic', {});
        console.log('Dynamic optimization stopped');
    },
    stepTime: () => {
        sendMessage('stepTime', {});
        console.log('Single time step');
    },
    getStatus: () => {
        const status = {
            isConnected,
            isDynamicRunning,
            currentTimeStep,
            currentTime,
            currentLoadValue,
            loadPointsSize: loadPoints.size,
            supportPointsSize: supportPoints.size,
            fatigueWarn: document.getElementById('fatigue-warn').textContent,
            fatigueDanger: document.getElementById('fatigue-danger').textContent,
            maxFatigueUI: document.getElementById('max-fatigue').textContent,
            timeStepUI: document.getElementById('time-step').textContent,
            iterCountUI: document.getElementById('iter-count').textContent
        };
        let maxF = 0, maxS = 0;
        let warnCount = 0, dangerCount = 0;
        for (let i = 0; i < voxelFatigue.length; i++) {
            if (voxelFatigue[i] > maxF) maxF = voxelFatigue[i];
            if (voxelFatigue[i] >= 0.7) dangerCount++;
            else if (voxelFatigue[i] >= 0.4) warnCount++;
        }
        for (let i = 0; i < voxelStress.length; i++) {
            if (voxelStress[i] > maxS) maxS = voxelStress[i];
        }
        status.maxFatigueInArray = maxF;
        status.maxStressInArray = maxS;
        status.warnCount = warnCount;
        status.dangerCount = dangerCount;
        console.log('Status:', status);
        return status;
    }
};
