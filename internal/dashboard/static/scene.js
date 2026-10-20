import { THREE, GLTFLoader } from "./vendor/three-bundle.min.js";

const sceneElement = document.getElementById("ambient-scene");
const canvasElement = document.getElementById("ambient-canvas");

let renderedFrames = 0;
try {
  Object.defineProperty(window, "__ambientFrames", {
    configurable: true,
    get: () => renderedFrames,
  });
} catch {
  window.__ambientFrames = renderedFrames;
}

if (sceneElement && canvasElement) {
  const VALID_STATES = new Set(["idle", "healthy", "degraded", "syncing", "empty"]);
  const PALETTES = {
    healthy: { rim: 0x6fd9b7, glow: 0x7de4c3, base: 0x030908, rate: 1.05 },
    degraded: { rim: 0xd08a56, glow: 0xd59b67, base: 0x0b0604, rate: 0.68 },
    syncing: { rim: 0x7fa6d9, glow: 0x9abbe4, base: 0x04070d, rate: 1.65 },
    neutral: { rim: 0x7d898c, glow: 0x8a9699, base: 0x070909, rate: 0.42 },
  };
  const FRAME_INTERVAL = 1000 / 30;
  const MAX_PARALLAX = 4;
  const DOME_RADIUS = 1.19;
  const motionQuery = window.matchMedia("(prefers-reduced-motion: reduce)");

  const target = {
    coverage: 0,
    emission: 0.055,
    grid: 16,
    rate: PALETTES.neutral.rate,
    rim: new THREE.Color(PALETTES.neutral.rim),
    glow: new THREE.Color(PALETTES.neutral.glow),
    base: new THREE.Color(PALETTES.neutral.base),
  };
  const current = {
    coverage: 0,
    emission: 0.055,
    grid: 16,
    rate: PALETTES.neutral.rate,
    rim: new THREE.Color(PALETTES.neutral.rim),
    glow: new THREE.Color(PALETTES.neutral.glow),
    base: new THREE.Color(PALETTES.neutral.base),
  };

  let renderer = null;
  let camera = null;
  let scene = null;
  let group = null;
  let domeUniforms = null;
  let pointUniforms = null;
  let sceneReady = false;
  let sizeValid = false;
  let firstDrawn = false;
  let failed = false;
  let contextLost = false;
  let fallbackNotified = false;
  let rafId = 0;
  let lastRender = 0;
  let elapsed = 0;
  let intersecting = true;
  let baseX = 0;
  let baseY = 0;
  let worldPerPixelX = 0.01;
  let worldPerPixelY = 0.01;
  const pointer = { x: 0, y: 0 };
  const pointerTarget = { x: 0, y: 0 };

  function asPercent(value) {
    if (typeof value !== "number" || !Number.isFinite(value)) return null;
    return Math.max(0, Math.min(100, value));
  }

  function applySnapshot(detail) {
    if (!detail || typeof detail !== "object") return;
    const sceneState = VALID_STATES.has(detail.state) ? detail.state : "idle";
    sceneElement.setAttribute("data-scene-state", sceneState);

    const coverage = asPercent(detail.coverage);
    const analyzable = asPercent(detail.analyzable);
    const missing = coverage === null || analyzable === null;
    const palette = missing ? PALETTES.neutral : PALETTES[sceneState] || PALETTES.neutral;
    const stateScale = sceneState === "healthy" ? 1 : sceneState === "syncing" ? 0.82 : sceneState === "degraded" ? 0.58 : 0.36;

    target.coverage = coverage === null ? 0 : coverage / 100;
    target.emission = missing ? 0.055 : 0.08 + (analyzable / 100) * 0.84 * stateScale;
    target.grid = missing ? 16 : 15 + (coverage / 100) * 25;
    target.rate = palette.rate;
    target.rim.setHex(palette.rim);
    target.glow.setHex(palette.glow);
    target.base.setHex(palette.base);
  }

  function snapshotCurrent() {
    current.coverage = target.coverage;
    current.emission = target.emission;
    current.grid = target.grid;
    current.rate = target.rate;
    current.rim.copy(target.rim);
    current.glow.copy(target.glow);
    current.base.copy(target.base);
  }

  function advanceLighting(delta) {
    const amount = 1 - Math.exp(-Math.max(delta, 0.001) * 3.4);
    current.coverage += (target.coverage - current.coverage) * amount;
    current.emission += (target.emission - current.emission) * amount;
    current.grid += (target.grid - current.grid) * amount;
    current.rate += (target.rate - current.rate) * amount;
    current.rim.lerp(target.rim, amount);
    current.glow.lerp(target.glow, amount);
    current.base.lerp(target.base, amount);
  }

  function notifyFallback(reason) {
    if (fallbackNotified) return;
    fallbackNotified = true;
    try {
      console.info(`Ambient scene fallback: ${reason}. Poster retained.`);
    } catch {}
  }

  function stopLoop() {
    if (rafId) cancelAnimationFrame(rafId);
    rafId = 0;
  }

  function degrade(reason) {
    if (failed) return;
    failed = true;
    stopLoop();
    canvasElement.classList.remove("is-ready");
    try {
      if (renderer && !contextLost) renderer.dispose();
    } catch {}
    notifyFallback(reason);
  }

  function lowPowerReason() {
    const connection = navigator.connection || navigator.webkitConnection || navigator.mozConnection;
    if (connection && connection.saveData === true) return "save-data";
    if (Number.isFinite(navigator.hardwareConcurrency) && navigator.hardwareConcurrency <= 2) return "low-cpu";
    return "";
  }

  const TERRAIN_GLSL = `
    float ambientHash(vec3 point) {
      point = fract(point * 0.3183099 + vec3(0.1, 0.2, 0.3));
      point *= 17.0;
      return fract(point.x * point.y * point.z * (point.x + point.y + point.z));
    }

    float ambientNoise(vec3 point) {
      vec3 cell = floor(point);
      vec3 local = fract(point);
      local = local * local * (3.0 - 2.0 * local);
      return mix(
        mix(mix(ambientHash(cell), ambientHash(cell + vec3(1.0, 0.0, 0.0)), local.x),
            mix(ambientHash(cell + vec3(0.0, 1.0, 0.0)), ambientHash(cell + vec3(1.0, 1.0, 0.0)), local.x), local.y),
        mix(mix(ambientHash(cell + vec3(0.0, 0.0, 1.0)), ambientHash(cell + vec3(1.0, 0.0, 1.0)), local.x),
            mix(ambientHash(cell + vec3(0.0, 1.0, 1.0)), ambientHash(cell + vec3(1.0, 1.0, 1.0)), local.x), local.y),
        local.z
      );
    }

    float ambientTerrain(vec3 direction, float time) {
      float broad = sin(direction.x * 3.7 + direction.y * 2.3 + direction.z * 5.1);
      broad += 0.5 * sin(direction.x * 7.4 - direction.y * 6.2 + direction.z * 4.8 + time * 0.09);
      broad += 0.25 * sin(direction.x * 14.0 + direction.y * 11.0 - direction.z * 12.0);
      return broad * 0.55 + (ambientNoise(direction * 24.0) - 0.5) * 0.28;
    }
  `;

  const DOME_VERTEX = `
    uniform float uTime;
    uniform float uCoverage;
    varying vec3 vWorldNormal;
    varying vec3 vViewDirection;
    varying vec3 vSphereDirection;
    varying float vRidge;
    ${TERRAIN_GLSL}

    void main() {
      vec3 direction = normalize(position);
      float macro = ambientTerrain(direction, uTime);
      float micro = ambientNoise(direction * 42.0 + vec3(0.0, uTime * 0.012, 0.0)) - 0.5;
      float amplitude = mix(0.014, 0.095, uCoverage);
      vec3 displaced = position + normal * (macro * amplitude + micro * mix(0.002, 0.006, uCoverage));
      vec4 worldPosition = modelMatrix * vec4(displaced, 1.0);
      vWorldNormal = normalize(normalMatrix * normal);
      vViewDirection = normalize(cameraPosition - worldPosition.xyz);
      vSphereDirection = direction;
      vRidge = macro * 0.5 + 0.5;
      gl_Position = projectionMatrix * viewMatrix * worldPosition;
    }
  `;

  const DOME_FRAGMENT = `
    uniform float uTime;
    uniform float uCoverage;
    uniform float uEmission;
    uniform float uGridDensity;
    uniform float uBreathRate;
    uniform vec3 uRimColor;
    uniform vec3 uGlowColor;
    uniform vec3 uBaseColor;
    uniform vec3 uHighlightColor;
    varying vec3 vWorldNormal;
    varying vec3 vViewDirection;
    varying vec3 vSphereDirection;
    varying float vRidge;

    float fineHash(vec2 point) {
      point = fract(point * vec2(123.34, 456.21));
      point += dot(point, point + 45.32);
      return fract(point.x * point.y);
    }

    float gridLine(float coordinate) {
      return 1.0 - smoothstep(0.0, 0.04, abs(fract(coordinate) - 0.5));
    }

    vec3 toneMap(vec3 color) {
      color *= 1.12;
      color = clamp((color * (2.51 * color + 0.03)) / (color * (2.43 * color + 0.59) + 0.14), 0.0, 1.0);
      return mix(color * 12.92, 1.055 * pow(color, vec3(0.4166667)) - 0.055, step(vec3(0.0031308), color));
    }

    void main() {
      vec3 normal = normalize(vWorldNormal);
      vec3 viewDirection = normalize(vViewDirection);
      float facing = clamp(dot(normal, viewDirection), 0.0, 1.0);
      float fresnel = pow(1.0 - facing, 4.5);
      float breath = 0.5 + 0.5 * sin(uTime * uBreathRate - 1.5707963);
      float frost = fineHash(floor(gl_FragCoord.xy * 0.72));

      vec3 direction = normalize(vSphereDirection);
      float latitude = asin(clamp(direction.y, -1.0, 1.0));
      float longitude = atan(direction.z, direction.x);
      float latitudeLine = gridLine((latitude / 3.14159265 + 0.5) * uGridDensity * 0.5);
      float longitudeLine = gridLine((longitude / 6.2831853 + 0.5) * uGridDensity);
      float polarFade = smoothstep(0.025, 0.2, length(direction.xz));
      float grid = (latitudeLine + longitudeLine) * polarFade * (0.22 + uCoverage * 0.78);

      vec3 copperDirection = normalize(vec3(-0.42, 0.82, 0.6));
      vec3 halfVector = normalize(copperDirection + viewDirection);
      float copper = pow(max(dot(normal, halfVector), 0.0), 92.0);
      copper *= smoothstep(0.08, 0.62, dot(normal, copperDirection));

      vec3 color = uBaseColor * (0.25 + frost * 0.08 + vRidge * 0.04);
      color += uRimColor * fresnel * (0.18 + uEmission * 0.56) * (0.82 + breath * 0.18);
      color += uGlowColor * grid * (0.016 + uEmission * 0.048) * (0.82 + breath * 0.18);
      color += uHighlightColor * copper * (1.25 + breath * 0.15);
      color += uGlowColor * uEmission * (0.0015 + fresnel * 0.012);
      color += uGlowColor * pow(max(dot(normal, halfVector), 0.0), 128.0) * frost * uEmission * 0.055;

      gl_FragColor = vec4(toneMap(color), 1.0);
    }
  `;

  const POINT_VERTEX = `
    uniform float uTime;
    uniform float uCoverage;
    uniform float uEmission;
    uniform float uBreathRate;
    uniform float uPixelRatio;
    uniform float uPointSize;
    attribute float aSpark;
    varying float vSpark;
    varying float vBreath;
    ${TERRAIN_GLSL}

    void main() {
      vec3 direction = normalize(position);
      float macro = ambientTerrain(direction, uTime);
      float amplitude = mix(0.014, 0.095, uCoverage);
      vec3 displaced = position + normal * (macro * amplitude + 0.012);
      vec4 viewPosition = modelViewMatrix * vec4(displaced, 1.0);
      gl_Position = projectionMatrix * viewPosition;
      float breath = 0.5 + 0.5 * sin(uTime * uBreathRate + aSpark * 6.2831853);
      gl_PointSize = uPointSize * uPixelRatio * (0.65 + aSpark * 1.05) * (0.65 + uEmission * 0.65) * (5.2 / max(1.0, -viewPosition.z));
      vSpark = aSpark;
      vBreath = breath;
    }
  `;

  const POINT_FRAGMENT = `
    uniform vec3 uGlowColor;
    uniform float uEmission;
    varying float vSpark;
    varying float vBreath;

    void main() {
      float radius = length(gl_PointCoord - vec2(0.5));
      float alpha = (1.0 - smoothstep(0.06, 0.5, radius)) * uEmission * (0.28 + vSpark * 0.72) * (0.72 + vBreath * 0.28);
      if (alpha < 0.004) discard;
      gl_FragColor = vec4(uGlowColor * (0.9 + vSpark * 0.5), alpha);
    }
  `;

  function createSharedUniforms() {
    const shared = {
      uTime: { value: 0 },
      uCoverage: { value: 0 },
      uEmission: { value: current.emission },
      uBreathRate: { value: current.rate },
    };
    domeUniforms = {
      ...shared,
      uGridDensity: { value: current.grid },
      uRimColor: { value: current.rim.clone() },
      uGlowColor: { value: current.glow.clone() },
      uBaseColor: { value: current.base.clone() },
      uHighlightColor: { value: new THREE.Color(0xd5a15f) },
    };
    pointUniforms = {
      ...shared,
      uGlowColor: { value: current.glow.clone() },
      uPixelRatio: { value: 1 },
      uPointSize: { value: 1.55 },
    };
  }

  function applyUniforms() {
    if (!domeUniforms || !pointUniforms) return;
    domeUniforms.uTime.value = elapsed;
    domeUniforms.uCoverage.value = current.coverage;
    domeUniforms.uEmission.value = current.emission;
    domeUniforms.uBreathRate.value = current.rate;
    domeUniforms.uGridDensity.value = current.grid;
    domeUniforms.uRimColor.value.copy(current.rim);
    domeUniforms.uGlowColor.value.copy(current.glow);
    domeUniforms.uBaseColor.value.copy(current.base);
    pointUniforms.uEmission.value = current.emission;
    pointUniforms.uGlowColor.value.copy(current.glow);
  }

  function buildSparkGeometry(sourceGeometry) {
    const position = sourceGeometry.getAttribute("position");
    const normal = sourceGeometry.getAttribute("normal");
    if (!position || !normal) throw new Error("dome geometry is incomplete");
    const step = 7;
    const count = Math.ceil(position.count / step);
    const positions = new Float32Array(count * 3);
    const normals = new Float32Array(count * 3);
    const sparks = new Float32Array(count);
    for (let output = 0; output < count; output += 1) {
      const input = output * step;
      const offset = output * 3;
      positions[offset] = position.getX(input);
      positions[offset + 1] = position.getY(input);
      positions[offset + 2] = position.getZ(input);
      normals[offset] = normal.getX(input);
      normals[offset + 1] = normal.getY(input);
      normals[offset + 2] = normal.getZ(input);
      let seed = (Math.imul(input + 1, 747796405) + 2891336453) >>> 0;
      seed = Math.imul(seed ^ (seed >>> 16), 2246822507) >>> 0;
      sparks[output] = 0.14 + (seed / 4294967295) * 0.86;
    }
    const geometry = new THREE.BufferGeometry();
    geometry.setAttribute("position", new THREE.BufferAttribute(positions, 3));
    geometry.setAttribute("normal", new THREE.BufferAttribute(normals, 3));
    geometry.setAttribute("aSpark", new THREE.BufferAttribute(sparks, 1));
    return geometry;
  }

  function disposeNodeMaterials(node) {
    const materials = Array.isArray(node.material) ? node.material : node.material ? [node.material] : [];
    for (const material of materials) material.dispose();
  }

  function applyLayout() {
    if (!renderer || !camera || !group) return;
    const width = sceneElement.clientWidth;
    const height = sceneElement.clientHeight;
    sizeValid = width >= 2 && height >= 2;
    if (!sizeValid) {
      stopLoop();
      return;
    }
    const pixelRatio = 1;
    renderer.setPixelRatio(pixelRatio);
    renderer.setSize(Math.max(1, Math.floor(width)), Math.max(1, Math.floor(height)), true);
    if (pointUniforms) pointUniforms.uPixelRatio.value = pixelRatio;

    const aspect = width / height;
    camera.aspect = aspect;
    camera.updateProjectionMatrix();
    const halfTangent = Math.tan((camera.fov * Math.PI) / 360);
    const distance = Math.max(
      DOME_RADIUS / (0.84 * halfTangent),
      DOME_RADIUS / (0.84 * halfTangent * Math.max(aspect, 0.3)),
    ) + DOME_RADIUS * 0.55;
    camera.position.set(0, 0.04, distance);
    camera.lookAt(0, 0, 0);
    const visibleHeight = 2 * distance * halfTangent;
    const visibleWidth = visibleHeight * aspect;
    baseX = visibleWidth * 0.09;
    baseY = visibleHeight * 0.02;
    worldPerPixelX = visibleWidth / width;
    worldPerPixelY = visibleHeight / height;
    group.position.set(baseX + pointer.x * worldPerPixelX, baseY + pointer.y * worldPerPixelY, 0);
  }

  function drawFrame() {
    if (failed || !renderer || !scene || !camera) return false;
    try {
      renderer.render(scene, camera);
      const context = renderer.getContext();
      if (context && context.isContextLost()) {
        degrade("webgl-context-lost");
        return false;
      }
      if (!firstDrawn && renderer.info.render.triangles === 0) {
        degrade("shader-compile");
        return false;
      }
      renderedFrames += 1;
      canvasElement.classList.add("is-ready");
      return true;
    } catch {
      degrade("render-failure");
      return false;
    }
  }

  function canRender() {
    return !failed && sceneReady && sizeValid && !document.hidden && intersecting;
  }

  function maybeRenderInitial() {
    if (!canRender()) {
      stopLoop();
      return false;
    }
    if (firstDrawn) return true;
    snapshotCurrent();
    elapsed = 0;
    applyUniforms();
    if (!drawFrame()) return false;
    firstDrawn = true;
    lastRender = performance.now();
    return true;
  }

  function scheduleLoop() {
    if (rafId || failed || motionQuery.matches || !canRender() || !firstDrawn) return;
    rafId = requestAnimationFrame(tick);
  }

  function tick(timestamp) {
    rafId = 0;
    if (failed || motionQuery.matches || !canRender()) return;
    if (timestamp - lastRender < FRAME_INTERVAL - 1) {
      scheduleLoop();
      return;
    }
    const delta = Math.min(0.1, Math.max(0.001, (timestamp - lastRender) / 1000));
    lastRender = timestamp;
    elapsed += delta;
    advanceLighting(delta);
    const pointerAmount = 1 - Math.exp(-delta * 6.5);
    pointer.x += (pointerTarget.x - pointer.x) * pointerAmount;
    pointer.y += (pointerTarget.y - pointer.y) * pointerAmount;
    group.position.set(baseX + pointer.x * worldPerPixelX, baseY + pointer.y * worldPerPixelY, 0);
    group.rotation.y += delta * 0.05;
    applyUniforms();
    if (!drawFrame()) return;
    scheduleLoop();
  }

  function syncAnimationState() {
    if (!sceneReady || failed) return;
    if (!canRender()) {
      stopLoop();
      return;
    }
    if (!firstDrawn && !maybeRenderInitial()) return;
    if (motionQuery.matches) {
      stopLoop();
      return;
    }
    scheduleLoop();
  }

  function resetPointer() {
    pointerTarget.x = 0;
    pointerTarget.y = 0;
  }

  function handlePointer(event) {
    if (event.pointerType && event.pointerType !== "mouse" && event.pointerType !== "pen") return;
    const bounds = sceneElement.getBoundingClientRect();
    if (
      bounds.width <= 0 ||
      bounds.height <= 0 ||
      event.clientX < bounds.left ||
      event.clientX > bounds.right ||
      event.clientY < bounds.top ||
      event.clientY > bounds.bottom
    ) {
      resetPointer();
      return;
    }
    const x = ((event.clientX - bounds.left) / bounds.width) * 2 - 1;
    const y = ((event.clientY - bounds.top) / bounds.height) * 2 - 1;
    pointerTarget.x = Math.max(-MAX_PARALLAX, Math.min(MAX_PARALLAX, x * MAX_PARALLAX));
    pointerTarget.y = Math.max(-MAX_PARALLAX, Math.min(MAX_PARALLAX, -y * MAX_PARALLAX));
  }

  async function initializeScene() {
    if (failed || sceneReady) return;
    const lowPower = lowPowerReason();
    if (lowPower) {
      degrade(lowPower);
      return;
    }
    const suppressCreationError = (event) => event.stopImmediatePropagation();
    canvasElement.addEventListener("webglcontextcreationerror", suppressCreationError, true);
    let context = null;
    try {
      context = canvasElement.getContext("webgl2", {
        alpha: true,
        antialias: false,
        depth: true,
        powerPreference: "low-power",
        stencil: false,
      });
    } catch {
      context = null;
    }
    if (!context) {
      canvasElement.removeEventListener("webglcontextcreationerror", suppressCreationError, true);
      degrade("webgl-unavailable");
      return;
    }
    try {
      renderer = new THREE.WebGLRenderer({
        canvas: canvasElement,
        context,
        alpha: true,
        antialias: false,
        powerPreference: "low-power",
        stencil: false,
      });
    } catch {
      canvasElement.removeEventListener("webglcontextcreationerror", suppressCreationError, true);
      degrade("webgl-startup");
      return;
    }
    canvasElement.removeEventListener("webglcontextcreationerror", suppressCreationError, true);
    renderer.setClearColor(0x000000, 0);
    renderer.toneMapping = THREE.ACESFilmicToneMapping;
    renderer.toneMappingExposure = 1.12;
    renderer.outputColorSpace = THREE.SRGBColorSpace;
    renderer.debug = renderer.debug || {};
    renderer.debug.checkShaderErrors = false;

    canvasElement.addEventListener(
      "webglcontextlost",
      (event) => {
        event.preventDefault();
        contextLost = true;
        degrade("webgl-context-lost");
      },
      false,
    );

    scene = new THREE.Scene();
    camera = new THREE.PerspectiveCamera(33, 1, 0.1, 30);
    group = new THREE.Group();
    group.rotation.set(0.13, -0.38, -0.07);
    scene.add(group);

    try {
      const loader = new GLTFLoader();
      const gltf = await loader.loadAsync("/assets/models/dome.glb");
      if (failed) return;
      let sourceMesh = null;
      gltf.scene.traverse((node) => {
        if (!sourceMesh && node.isMesh && node.geometry) sourceMesh = node;
      });
      if (!sourceMesh) throw new Error("dome mesh missing");
      const geometry = sourceMesh.geometry;
      if (!geometry.getAttribute("normal")) geometry.computeVertexNormals();
      disposeNodeMaterials(sourceMesh);

      createSharedUniforms();
      const domeMaterial = new THREE.ShaderMaterial({
        uniforms: domeUniforms,
        vertexShader: DOME_VERTEX,
        fragmentShader: DOME_FRAGMENT,
        side: THREE.FrontSide,
      });
      const dome = new THREE.Mesh(geometry, domeMaterial);
      dome.frustumCulled = false;
      group.add(dome);

      const sparkMaterial = new THREE.ShaderMaterial({
        uniforms: pointUniforms,
        vertexShader: POINT_VERTEX,
        fragmentShader: POINT_FRAGMENT,
        transparent: true,
        depthWrite: false,
        depthTest: true,
        blending: THREE.AdditiveBlending,
        toneMapped: false,
      });
      const sparks = new THREE.Points(buildSparkGeometry(geometry), sparkMaterial);
      sparks.frustumCulled = false;
      group.add(sparks);

      group.scale.set(1.18, 1.0, 1.08);
      sceneReady = true;
      snapshotCurrent();
      applyUniforms();
      applyLayout();
      syncAnimationState();
    } catch {
      degrade("model-load");
    }
  }

  document.addEventListener("stockwatcher:snapshot", (event) => applySnapshot(event.detail));
  if (window.__stockWatcherAmbient) applySnapshot(window.__stockWatcherAmbient);

  let initializationScheduled = false;
  function scheduleInitialization() {
    if (initializationScheduled) return;
    initializationScheduled = true;
    const start = () => {
      initializeScene().catch(() => degrade("initialization"));
    };
    if (typeof window.requestIdleCallback === "function") {
      window.requestIdleCallback(start, { timeout: 1200 });
    } else {
      window.setTimeout(start, 0);
    }
  }

  if (document.readyState === "complete") {
    scheduleInitialization();
  } else {
    window.addEventListener("load", scheduleInitialization, { once: true });
  }

  const initialBounds = sceneElement.getBoundingClientRect();
  intersecting =
    initialBounds.width > 0 &&
    initialBounds.height > 0 &&
    initialBounds.bottom > 0 &&
    initialBounds.top < window.innerHeight &&
    initialBounds.right > 0 &&
    initialBounds.left < window.innerWidth;

  if (typeof ResizeObserver === "function") {
    new ResizeObserver(() => {
      applyLayout();
      if (sceneReady && !firstDrawn) syncAnimationState();
      else if (sceneReady && !motionQuery.matches) scheduleLoop();
    }).observe(sceneElement);
  }
  window.addEventListener("resize", () => {
    applyLayout();
    if (sceneReady && !firstDrawn) syncAnimationState();
    else if (sceneReady && !motionQuery.matches) scheduleLoop();
  });

  if (typeof IntersectionObserver === "function") {
    new IntersectionObserver(
      (entries) => {
        intersecting = entries.some((entry) => entry.isIntersecting);
        syncAnimationState();
      },
      { threshold: 0.01 },
    ).observe(sceneElement);
  }

  document.addEventListener("visibilitychange", syncAnimationState);
  document.addEventListener("pointermove", handlePointer, { passive: true });
  document.addEventListener("pointerleave", resetPointer, { passive: true });
  window.addEventListener("blur", resetPointer, { passive: true });
  if (typeof motionQuery.addEventListener === "function") {
    motionQuery.addEventListener("change", () => {
      if (motionQuery.matches) {
        stopLoop();
        if (!firstDrawn) syncAnimationState();
      } else {
        syncAnimationState();
      }
    });
  }
}
