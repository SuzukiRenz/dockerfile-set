use axum::http::{HeaderMap, Method, Uri};
use chrono::{NaiveDateTime, Utc};
use hmac::{Hmac, Mac};
use sha2::{Digest, Sha256};
use subtle::ConstantTimeEq;

type HmacSha256 = Hmac<Sha256>;

/// Pull the access key id out of the Authorization header without validating anything
/// yet -- used to look up which credential's secret key to check the signature against.
pub fn extract_access_key(headers: &HeaderMap) -> Option<String> {
    let auth = headers.get("authorization")?.to_str().ok()?;
    let rest = auth.strip_prefix("AWS4-HMAC-SHA256 ")?;
    for item in rest.split(',') {
        let mut kv = item.trim().splitn(2, '=');
        if kv.next()? == "Credential" {
            return kv.next()?.split('/').next().map(|s| s.to_owned());
        }
    }
    None
}

/// AWS Signature V4 verifier for header-authenticated requests.
/// Query-string presigning is intentionally rejected until a complete expiry policy is added.
pub fn authorize(method: &Method, uri: &Uri, headers: &HeaderMap, payload_hash: &str, region: &str, access_key: &str, secret_key: &str, skew_seconds: i64) -> bool {
    let Some(auth) = headers.get("authorization").and_then(|v| v.to_str().ok()) else { return false; };
    let Some(rest) = auth.strip_prefix("AWS4-HMAC-SHA256 ") else { return false; };
    let mut credential = ""; let mut signed_headers = ""; let mut signature = "";
    for item in rest.split(',') {
        let mut kv = item.trim().splitn(2, '=');
        match (kv.next().unwrap_or(""), kv.next().unwrap_or("")) {
            ("Credential", v) => credential = v,
            ("SignedHeaders", v) => signed_headers = v,
            ("Signature", v) => signature = v,
            _ => {}
        }
    }
    let mut cp = credential.split('/');
    let access = cp.next().unwrap_or(""); let date = cp.next().unwrap_or("");
    let cred_region = cp.next().unwrap_or(""); let service = cp.next().unwrap_or("");
    if access != access_key || cred_region != region || service != "s3" || cp.next() != Some("aws4_request") || date.len() != 8 || signature.len() != 64 { return false; }
    let amz_date = headers.get("x-amz-date").and_then(|v|v.to_str().ok()).unwrap_or("");
    if amz_date.len() != 16 || !amz_date.starts_with(date) { return false; }
    if let Ok(t) = NaiveDateTime::parse_from_str(amz_date, "%Y%m%dT%H%M%S") {
        let age = (Utc::now().naive_utc() - t).num_seconds().abs(); if age > skew_seconds { return false; }
    } else { return false; }
    let signed_payload_hash = headers.get("x-amz-content-sha256").and_then(|v|v.to_str().ok()).unwrap_or("");
    if signed_payload_hash == "UNSIGNED-PAYLOAD" || signed_payload_hash != payload_hash { return false; }
    if payload_hash.len() != 64 || !payload_hash.bytes().all(|b| b.is_ascii_hexdigit()) { return false; }
    let names: Vec<&str> = signed_headers.split(';').filter(|x| !x.is_empty()).collect();
    if names.is_empty() || names.windows(2).any(|w| w[0] >= w[1]) { return false; }
    let mut canonical_headers = String::new();
    for name in &names {
        let Some(v) = headers.get(*name).and_then(|v|v.to_str().ok()) else { return false; };
        canonical_headers.push_str(name); canonical_headers.push(':'); canonical_headers.push_str(&normalize(v)); canonical_headers.push('\n');
    }
    let canonical_query = canonical_query(uri);
    let canonical = format!("{}\n{}\n{}\n{}\n{}\n{}", method.as_str(), canonical_uri(uri.path()), canonical_query, canonical_headers, signed_headers, payload_hash);
    let scope = format!("{date}/{cred_region}/{service}/aws4_request");
    let hashed = hex(&Sha256::digest(canonical.as_bytes()));
    let string_to_sign = format!("AWS4-HMAC-SHA256\n{amz_date}\n{scope}\n{hashed}");
    let k_date = hmac(format!("AWS4{secret_key}").as_bytes(), date.as_bytes());
    let k_region = hmac(&k_date, cred_region.as_bytes()); let k_service = hmac(&k_region, service.as_bytes()); let k_signing = hmac(&k_service, b"aws4_request");
    let expected = hex(&hmac(&k_signing, string_to_sign.as_bytes())); expected.as_bytes().ct_eq(signature.as_bytes()).into()
}
fn normalize(v: &str) -> String { v.split_whitespace().collect::<Vec<_>>().join(" ") }
fn canonical_uri(path: &str) -> String { if path.is_empty() { "/".into() } else { path.to_owned() } }
fn canonical_query(uri: &Uri) -> String {
    let mut q: Vec<(String,String)> = urlencoding::decode(uri.query().unwrap_or("")).unwrap_or_default().split('&').filter(|x|!x.is_empty()).map(|p| { let mut s=p.splitn(2,'='); (s.next().unwrap_or("").to_owned(),s.next().unwrap_or("").to_owned()) }).collect();
    q.sort(); q.into_iter().map(|(k,v)| format!("{}={}", urlencoding::encode(&k), urlencoding::encode(&v))).collect::<Vec<_>>().join("&")
}
fn hmac(key: &[u8], data: &[u8]) -> Vec<u8> { let mut m=HmacSha256::new_from_slice(key).expect("valid hmac key"); m.update(data); m.finalize().into_bytes().to_vec() }
fn hex(b: &[u8]) -> String { b.iter().map(|x|format!("{x:02x}")).collect() }
