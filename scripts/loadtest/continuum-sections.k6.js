import http from "k6/http";
import { check, group, sleep } from "k6";

const BASE_URL = (__ENV.BASE_URL || "http://localhost:8080/api/v2").replace(
  /\/$/,
  "",
);
const TOKEN = __ENV.TOKEN || "";
// Every v2 section read is profile-scoped, so the run is meaningless without one.
const PROFILE_ID = __ENV.PROFILE_ID || "";
if (!PROFILE_ID) {
  throw new Error(
    "PROFILE_ID is required: v2 section endpoints need X-Profile-Id",
  );
}
// v2 identifiers are opaque strings; send them back exactly as the API issued them.
const LIBRARY_IDS = (__ENV.LIBRARY_IDS || "")
  .split(",")
  .map((value) => value.trim())
  .filter((value) => value.length > 0);

const HOME_SECTION_CONCURRENCY = Number(__ENV.HOME_SECTION_CONCURRENCY || 5);
const LIBRARY_SECTION_CONCURRENCY = Number(
  __ENV.LIBRARY_SECTION_CONCURRENCY || 4,
);

export const options = {
  scenarios: {
    home_surface: {
      executor: "constant-vus",
      vus: Number(__ENV.HOME_VUS || 3),
      duration: __ENV.HOME_DURATION || "2m",
      exec: "homeSurface",
    },
    library_surface: {
      executor: "constant-vus",
      vus: Number(__ENV.LIBRARY_VUS || 2),
      duration: __ENV.LIBRARY_DURATION || "2m",
      exec: "librarySurface",
    },
    aggregate_sections: {
      executor: "constant-vus",
      vus: Number(__ENV.AGGREGATE_VUS || 1),
      duration: __ENV.AGGREGATE_DURATION || "1m",
      exec: "aggregateSections",
    },
  },
  thresholds: {
    http_req_failed: ["rate<0.01"],
    "http_req_duration{route:home_section_items}": ["p(95)<3000"],
    "http_req_duration{route:library_section_items}": ["p(95)<2000"],
    "http_req_duration{route:aggregate_sections}": ["p(95)<6000"],
  },
};

function headers() {
  const result = {
    Accept: "application/json",
    "Content-Type": "application/json",
  };
  if (TOKEN) {
    result.Authorization = `Bearer ${TOKEN}`;
  }
  result["X-Profile-Id"] = PROFILE_ID;
  return result;
}

function getJSON(path, tags) {
  const response = http.get(`${BASE_URL}${path}`, { headers: headers(), tags });
  check(response, {
    [`GET ${path} returned 2xx`]: (res) =>
      res.status >= 200 && res.status < 300,
  });
  if (response.status < 200 || response.status >= 300) {
    return null;
  }
  return response.json();
}

function fetchSectionBatches(paths, concurrency, route) {
  for (let i = 0; i < paths.length; i += concurrency) {
    const batch = paths
      .slice(i, i + concurrency)
      .map((path) => [
        "GET",
        `${BASE_URL}${path}`,
        null,
        { headers: headers(), tags: { route } },
      ]);
    const responses = http.batch(batch);
    responses.forEach((response) => {
      check(response, {
        [`${route} returned 2xx`]: (res) =>
          res.status >= 200 && res.status < 300,
      });
    });
  }
}

export function homeSurface() {
  group("home layout plus item batches", () => {
    const layout = getJSON("/home/layout", { route: "home_layout" });
    const sections = Array.isArray(layout?.sections) ? layout.sections : [];
    const paths = sections.map(
      (section) => `/home/sections/${section.id}/items`,
    );
    fetchSectionBatches(paths, HOME_SECTION_CONCURRENCY, "home_section_items");
  });
  sleep(1);
}

export function librarySurface() {
  if (LIBRARY_IDS.length === 0) {
    return;
  }
  const libraryID = LIBRARY_IDS[__VU % LIBRARY_IDS.length];
  group("library layout plus item batches", () => {
    const layout = getJSON(`/library/${libraryID}/layout`, {
      route: "library_layout",
    });
    const sections = Array.isArray(layout?.sections) ? layout.sections : [];
    const paths = sections.map(
      (section) => `/library/${libraryID}/sections/${section.id}/items`,
    );
    fetchSectionBatches(
      paths,
      LIBRARY_SECTION_CONCURRENCY,
      "library_section_items",
    );
  });
  sleep(1);
}

export function aggregateSections() {
  group("aggregate section endpoints", () => {
    getJSON("/home/sections", { route: "aggregate_sections" });
    LIBRARY_IDS.forEach((libraryID) => {
      getJSON(`/library/${libraryID}/sections`, {
        route: "aggregate_sections",
      });
    });
  });
  sleep(2);
}
