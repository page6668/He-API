/**
 * Test implementation for Story 1.3: 阿里云 VPC + K8s 集群 + 基础网络（Terraform）
 *
 * Implemented from the QA-generated skeleton (Turing, 2026-05-11). Every
 * static (Unit) scenario has a real assertion; every runtime scenario
 * (Integration / E2E / runtime-only Blind-Spot) stays `test.skip` with a
 * pointer to `docs/dev/logs/1.3-dev-log.md` §6 (Operator Hand-off).
 *
 * Static scenarios parse Terraform HCL via regex (HCL is not a YAML/JSON
 * subset; raw text + regex matches the convention SM/Architect already use
 * in deliverable_bindings.verify), and Helm/K8s YAML via `yaml`.
 *
 * Test Design: docs/qa/assessments/1.3-test-design-20260511.md
 */

import { describe, expect, test } from 'vitest';
import { readFileSync, statSync, existsSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { parse as parseYaml, parseAllDocuments } from 'yaml';

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);
const REPO_ROOT = resolve(__dirname, '..', '..', '..');

const repoPath = (rel: string): string => resolve(REPO_ROOT, rel);
const readRepoFile = (rel: string): string =>
  readFileSync(repoPath(rel), 'utf8');
const repoFileExists = (rel: string): boolean => existsSync(repoPath(rel));
const loadYaml = <T = unknown>(rel: string): T =>
  parseYaml(readRepoFile(rel)) as T;
const loadAllYaml = (rel: string): unknown[] =>
  parseAllDocuments(readRepoFile(rel)).map((d) => d.toJSON());

// ---------- shared HCL fixtures (read once per file) ----------
const bootstrapSh = readRepoFile('scripts/infra/bootstrap-state-backend.sh');
const backendTf = readRepoFile('infra/terraform/backend.tf');
const vpcMain = readRepoFile('infra/terraform/modules/vpc/main.tf');
const vpcVars = readRepoFile('infra/terraform/modules/vpc/variables.tf');
const vpcOutputs = readRepoFile('infra/terraform/modules/vpc/outputs.tf');
const ackMain = readRepoFile('infra/terraform/modules/ack/main.tf');
const ackVars = readRepoFile('infra/terraform/modules/ack/variables.tf');
const ackOutputs = readRepoFile('infra/terraform/modules/ack/outputs.tf');
const acrMain = readRepoFile('infra/terraform/modules/acr/main.tf');
const acrOutputs = readRepoFile('infra/terraform/modules/acr/outputs.tf');
const stagingMain = readRepoFile('infra/terraform/envs/staging/main.tf');
const stagingTfvarsExample = readRepoFile(
  'infra/terraform/envs/staging/terraform.tfvars.example',
);
const prodMain = readRepoFile('infra/terraform/envs/prod/main.tf');
const prodTfvarsExample = readRepoFile(
  'infra/terraform/envs/prod/terraform.tfvars.example',
);
const gitignoreTxt = readRepoFile('.gitignore');
const readmeMd = readRepoFile('README.md');
const tflintHcl = readRepoFile('.tflint.hcl');
const infraLintYmlText = readRepoFile('.github/workflows/infra-lint.yml');

// ============================================================
// AC1: K8s 集群可访问
// ============================================================

describe('AC1 / T0: Terraform State Backend Bootstrap', () => {
  test('1.3-UNIT-001: scripts/infra/bootstrap-state-backend.sh exists and is executable', () => {
    expect(repoFileExists('scripts/infra/bootstrap-state-backend.sh')).toBe(true);
    const mode = statSync(repoPath('scripts/infra/bootstrap-state-backend.sh')).mode;
    // user-executable bit (0o100)
    expect(mode & 0o100).not.toBe(0);
  });

  test('1.3-UNIT-002: bootstrap script creates OSS bucket with versioning + KMS encryption + restricted bucket policy', () => {
    // OSS bucket create command — accept either `create-bucket` or first-time mkdir form.
    expect(bootstrapSh).toMatch(/aliyun\s+oss\s+create-bucket/);
    // versioning enabled (explicit put-bucket-versioning --status Enabled)
    expect(bootstrapSh).toMatch(/put-bucket-versioning[\s\S]+--status\s+Enabled/);
    // SSE-KMS
    expect(bootstrapSh).toMatch(/put-bucket-encryption[\s\S]+--sse-algorithm\s+KMS/);
    expect(bootstrapSh).toMatch(/--kms-master-key-id/);
    // bucket policy referencing the tfstate-operator RAM principal
    expect(bootstrapSh).toMatch(/put-bucket-policy/);
    expect(bootstrapSh).toMatch(/tfstate-operator/);
  });

  test('1.3-UNIT-003: bootstrap script creates KMS key with rotation, alias, and ENCRYPT_DECRYPT usage', () => {
    expect(bootstrapSh).toMatch(/aliyun\s+kms\s+CreateKey/);
    expect(bootstrapSh).toMatch(/--KeyUsage\s+ENCRYPT_DECRYPT/);
    expect(bootstrapSh).toMatch(/alias\/he-api-tfstate-\$\{ENV\}/);
    expect(bootstrapSh).toMatch(/--EnableKeyRotation\s+true/);
    expect(bootstrapSh).toMatch(/365d/);
  });

  test('1.3-UNIT-004: bootstrap script creates TableStore + terraform-lock table with PK=LockID:string', () => {
    expect(bootstrapSh).toMatch(/aliyun\s+ots\s+CreateInstance/);
    expect(bootstrapSh).toMatch(/aliyun\s+ots\s+CreateTable/);
    expect(bootstrapSh).toMatch(/terraform-lock/);
    expect(bootstrapSh).toMatch(/"Name"\s*:\s*"LockID"/);
    expect(bootstrapSh).toMatch(/"Type"\s*:\s*"string"/);
  });

  test('1.3-UNIT-005: infra/terraform/backend.tf declares OSS backend with KMS + TableStore lock + encrypt=true', () => {
    expect(backendTf).toMatch(
      /terraform\s*\{[\s\S]+backend\s+"oss"[\s\S]+bucket\s*=\s*"he-api-tfstate-staging-sh"[\s\S]+tablestore_table\s*=\s*"terraform-lock"[\s\S]+encrypt\s*=\s*true/,
    );
  });

  test('1.3-UNIT-006: bootstrap script greps infra/ for plaintext keys/passwords and exits non-0 if found', () => {
    expect(bootstrapSh).toMatch(/grep\s+-rEn/);
    // Literal grep alternation pattern that the bash script applies to infra/ tree.
    expect(bootstrapSh).toContain('access[_-]?key');
    expect(bootstrapSh).toContain('secret[_-]?key');
    expect(bootstrapSh).toContain('password');
    expect(bootstrapSh).toMatch(/\binfra\//);
    // exit non-0 (`exit 5` in the implementation, but any non-zero is acceptable).
    expect(bootstrapSh).toMatch(/exit\s+[1-9]/);
  });

  test('1.3-UNIT-007: bootstrap bucket-name pattern matches he-api-tfstate-${env}-${region_short}', () => {
    expect(bootstrapSh).toMatch(/he-api-tfstate-\$\{ENV\}-\$\{REGION_SHORT\}/);
  });

  test('1.3-UNIT-008: README "Bootstrap Sequence" section states "ops only, once, with admin creds; CI/Dev have no permission"', () => {
    expect(readmeMd).toMatch(/Bootstrap Sequence/);
    expect(readmeMd).toMatch(/运维一次性执行/);
    // "CI / Dev ... 均无权限" may span lines (Markdown blockquote wrapping); allow [\s\S].
    expect(readmeMd).toMatch(/CI\s*\/\s*Dev[\s\S]{0,80}无权限/);
  });
});

describe('AC1 / T1: VPC module (infra/terraform/modules/vpc/)', () => {
  test('1.3-UNIT-010: vpc/main.tf declares vpc + 3 vswitches + nat + eip + default deny-all SG', () => {
    expect((vpcMain.match(/resource\s+"alicloud_vpc"\s+"/g) || []).length).toBe(1);
    expect((vpcMain.match(/resource\s+"alicloud_vswitch"\s+"/g) || []).length).toBe(1);
    // 3 vSwitches via count = length(var.vswitch_cidrs) (variables.tf validation = 3)
    expect(vpcMain).toMatch(/count\s*=\s*length\(var\.vswitch_cidrs\)/);
    expect((vpcMain.match(/resource\s+"alicloud_nat_gateway"\s+"/g) || []).length).toBe(1);
    expect((vpcMain.match(/resource\s+"alicloud_eip"\s+"/g) || []).length).toBe(1);
    expect((vpcMain.match(/resource\s+"alicloud_security_group"\s+"default"/g) || []).length).toBe(1);
  });

  test('1.3-UNIT-011: vpc/variables.tf declares only vpc_cidr / vswitch_cidrs / availability_zones / tags (NO vpn_allowed_cidrs)', () => {
    expect(vpcVars).toMatch(/variable\s+"vpc_cidr"/);
    expect(vpcVars).toMatch(/variable\s+"vswitch_cidrs"/);
    expect(vpcVars).toMatch(/variable\s+"availability_zones"/);
    expect(vpcVars).toMatch(/variable\s+"tags"/);
    expect(vpcVars).not.toMatch(/variable\s+"vpn_allowed_cidrs"/);
  });

  test('1.3-UNIT-012: vpc/outputs.tf exports vpc_id / vswitch_ids / nat_gateway_id / default_security_group_id', () => {
    expect(vpcOutputs).toMatch(/output\s+"vpc_id"/);
    expect(vpcOutputs).toMatch(/output\s+"vswitch_ids"/);
    expect(vpcOutputs).toMatch(/output\s+"nat_gateway_id"/);
    expect(vpcOutputs).toMatch(/output\s+"default_security_group_id"/);
  });

  test('1.3-UNIT-013: vpc/main.tf has NO SG rule with cidr_ip=0.0.0.0/0 + port_range=22/22', () => {
    // Either no 0.0.0.0/0 at all, or any occurrence is NOT paired with port 22.
    const lines = vpcMain.split('\n');
    let inSgRule = false;
    let bufferedRule = '';
    for (const line of lines) {
      if (/resource\s+"alicloud_security_group_rule"/.test(line)) inSgRule = true;
      if (inSgRule) bufferedRule += line + '\n';
      if (inSgRule && line.trim() === '}') {
        const hasPublic = /cidr_ip\s*=\s*"0\.0\.0\.0\/0"/.test(bufferedRule);
        const hasSsh = /port_range\s*=\s*"22\/22"/.test(bufferedRule);
        expect(hasPublic && hasSsh).toBe(false);
        inSgRule = false;
        bufferedRule = '';
      }
    }
  });

  test('1.3-UNIT-014: vpc/main.tf has NO SG rule with cidr_ip=0.0.0.0/0 + port containing 6443', () => {
    const lines = vpcMain.split('\n');
    let inSgRule = false;
    let bufferedRule = '';
    for (const line of lines) {
      if (/resource\s+"alicloud_security_group_rule"/.test(line)) inSgRule = true;
      if (inSgRule) bufferedRule += line + '\n';
      if (inSgRule && line.trim() === '}') {
        const hasPublic = /cidr_ip\s*=\s*"0\.0\.0\.0\/0"/.test(bufferedRule);
        const hasApi = /6443/.test(bufferedRule);
        expect(hasPublic && hasApi).toBe(false);
        inSgRule = false;
        bufferedRule = '';
      }
    }
  });

  test('1.3-UNIT-015: vpc/main.tf SG ingress rules cite only VPC-internal CIDR (var.vpc_cidr reference)', () => {
    // Every ingress rule MUST reference var.vpc_cidr — no public/literal CIDRs.
    const ruleBlocks = vpcMain.split(/resource\s+"alicloud_security_group_rule"/).slice(1);
    expect(ruleBlocks.length).toBeGreaterThan(0);
    for (const block of ruleBlocks) {
      const ruleBody = block.split(/\n\}/)[0];
      if (/type\s*=\s*"ingress"/.test(ruleBody)) {
        expect(ruleBody).toMatch(/cidr_ip\s*=\s*var\.vpc_cidr/);
      }
    }
  });

  test('1.3-UNIT-016: vpc/main.tf alicloud_eip is associated to NAT Gateway via alicloud_eip_association', () => {
    expect(vpcMain).toMatch(/resource\s+"alicloud_eip_association"\s+"nat"/);
    expect(vpcMain).toMatch(/instance_type\s*=\s*"Nat"/);
    expect(vpcMain).toMatch(/allocation_id\s*=\s*alicloud_eip\.nat\.id/);
    expect(vpcMain).toMatch(/instance_id\s*=\s*alicloud_nat_gateway\.this\.id/);
  });
});

describe('AC1 / T2: ACK module (infra/terraform/modules/ack/) — Path A2', () => {
  test('1.3-UNIT-020: ack/variables.tf declares api_server_public_access_enabled (bool, default true)', () => {
    expect(ackVars).toMatch(
      /variable\s+"api_server_public_access_enabled"\s*\{[\s\S]+type\s*=\s*bool[\s\S]+default\s*=\s*true/,
    );
  });

  test('1.3-UNIT-021: ack/variables.tf declares api_server_public_access_allowed_cidrs (list(string), default [])', () => {
    expect(ackVars).toMatch(
      /variable\s+"api_server_public_access_allowed_cidrs"\s*\{[\s\S]+type\s*=\s*list\(string\)[\s\S]+default\s*=\s*\[\]/,
    );
  });

  test('1.3-UNIT-022: ack/main.tf maps cluster_endpoint_public_access ← var.api_server_public_access_enabled', () => {
    expect(ackMain).toMatch(
      /cluster_endpoint_public_access\s*=\s*var\.api_server_public_access_enabled/,
    );
  });

  test('1.3-UNIT-023: ack/main.tf maps cluster_endpoint_public_access_acl_cidrs ← var.api_server_public_access_allowed_cidrs', () => {
    expect(ackMain).toMatch(
      /cluster_endpoint_public_access_acl_cidrs\s*=\s*var\.api_server_public_access_allowed_cidrs/,
    );
  });

  test('1.3-UNIT-024: ack/main.tf has precondition enforcing length(allowed_cidrs)>0 when enabled=true', () => {
    expect(ackMain).toMatch(
      /precondition[\s\S]+length\(var\.api_server_public_access_allowed_cidrs\)\s*>\s*0/,
    );
  });

  test('1.3-UNIT-025: ack/main.tf does NOT contain literal "cluster_endpoint_public_access = true" (must be variable-driven)', () => {
    // Strip leading whitespace per line, then search for the literal hardcoded form.
    expect(ackMain).not.toMatch(/cluster_endpoint_public_access\s*=\s*true\b/);
  });

  test('1.3-UNIT-026: ack/main.tf cluster_endpoint_private_access_enabled = true', () => {
    expect(ackMain).toMatch(/cluster_endpoint_private_access_enabled\s*=\s*true/);
  });

  test('1.3-UNIT-027: ack/main.tf K8s version ≥ 1.29; service_cidr=172.16.0.0/16; pod_cidr=172.20.0.0/16', () => {
    // version is variable-driven; default in variables.tf must start with 1.29 or later.
    expect(ackVars).toMatch(
      /variable\s+"k8s_version"[\s\S]+default\s*=\s*"1\.(29|30|31|32|33)/,
    );
    expect(ackVars).toMatch(/variable\s+"service_cidr"[\s\S]+default\s*=\s*"172\.16\.0\.0\/16"/);
    expect(ackVars).toMatch(/variable\s+"pod_cidr"[\s\S]+default\s*=\s*"172\.20\.0\.0\/16"/);
  });

  test('1.3-UNIT-028: ack addons enable flannel + csi-plugin + metrics-server; do NOT enable nginx-ingress', () => {
    expect(ackMain).toMatch(/addons\s*\{\s*name\s*=\s*"flannel"\s*\}/);
    expect(ackMain).toMatch(/addons\s*\{\s*name\s*=\s*"csi-plugin"\s*\}/);
    expect(ackMain).toMatch(/addons\s*\{\s*name\s*=\s*"metrics-server"\s*\}/);
    expect(ackMain).not.toMatch(/addons\s*\{\s*name\s*=\s*"nginx-ingress"/);
  });

  test('1.3-UNIT-029: ack/outputs.tf exports cluster_id + kubeconfig (sensitive=true) + intranet/internet endpoints', () => {
    expect(ackOutputs).toMatch(/output\s+"cluster_id"/);
    expect(ackOutputs).toMatch(
      /output\s+"kubeconfig"\s*\{[\s\S]+sensitive\s*=\s*true/,
    );
    expect(ackOutputs).toMatch(/output\s+"api_server_endpoint_intranet"/);
    expect(ackOutputs).toMatch(/output\s+"api_server_endpoint_internet"/);
  });

  test('1.3-UNIT-030: ack/main.tf worker pool spreads across var.vswitch_ids; instance_type is variable-driven', () => {
    expect(ackMain).toMatch(/worker_vswitch_ids\s*=\s*var\.vswitch_ids/);
    expect(ackMain).toMatch(/worker_instance_types\s*=\s*\[var\.worker_instance_type\]/);
  });
});

describe('AC1 / T3: ACR module (infra/terraform/modules/acr/) — EE Basic', () => {
  test('1.3-UNIT-040: acr/main.tf uses alicloud_cr_ee_instance (NOT Personal-only resource types)', () => {
    expect(acrMain).toMatch(/resource\s+"alicloud_cr_ee_instance"\s+"this"/);
    // Personal-tier resources MUST NOT appear.
    expect(acrMain).not.toMatch(/resource\s+"alicloud_cr_namespace"\b/);
    expect(acrMain).not.toMatch(/resource\s+"alicloud_cr_repo"\b/);
  });

  test('1.3-UNIT-041: alicloud_cr_ee_instance has instance_type=Basic, payment_type=Subscription, period=1, AutoRenewal', () => {
    expect(acrMain).toMatch(/instance_type\s*=\s*"Basic"/);
    expect(acrMain).toMatch(/payment_type\s*=\s*"Subscription"/);
    expect(acrMain).toMatch(/period\s*=\s*1/);
    expect(acrMain).toMatch(/renewal_status\s*=\s*"AutoRenewal"/);
  });

  test('1.3-UNIT-042: alicloud_cr_ee_namespace name=he-api, auto_create=false, default_visibility=PRIVATE', () => {
    expect(acrMain).toMatch(
      /resource\s+"alicloud_cr_ee_namespace"\s+"he_api"\s*\{[\s\S]+name\s*=\s*"he-api"[\s\S]+auto_create\s*=\s*false[\s\S]+default_visibility\s*=\s*"PRIVATE"/,
    );
  });

  test('1.3-UNIT-043: alicloud_cr_ee_repo name=api-gateway, repo_type=PRIVATE, namespace references EE namespace', () => {
    expect(acrMain).toMatch(
      /resource\s+"alicloud_cr_ee_repo"\s+"api_gateway"\s*\{[\s\S]+name\s*=\s*"api-gateway"[\s\S]+repo_type\s*=\s*"PRIVATE"/,
    );
    expect(acrMain).toMatch(/namespace\s*=\s*alicloud_cr_ee_namespace\.he_api\.name/);
  });

  test('1.3-UNIT-044: acr/outputs.tf acr_endpoint = "${instance.name}-registry.${region}.cr.aliyuncs.com" (EE independent endpoint)', () => {
    expect(acrOutputs).toMatch(
      /output\s+"acr_endpoint"\s*\{[\s\S]+value\s*=\s*"\$\{alicloud_cr_ee_instance\.this\.name\}-registry\.\$\{var\.region\}\.cr\.aliyuncs\.com"/,
    );
  });

  test('1.3-UNIT-045: acr/outputs.tf does NOT export acr_password (only username_path + endpoint)', () => {
    expect(acrOutputs).not.toMatch(/output\s+"acr_password"/);
    expect(acrOutputs).toMatch(/output\s+"acr_username_path"/);
    expect(acrOutputs).toMatch(/output\s+"acr_endpoint"/);
  });
});

describe('AC1 / T4: oss-state meta module', () => {
  test('1.3-UNIT-050: oss-state/README.md exists and explains bootstrap order (OSS+TableStore+KMS)', () => {
    expect(repoFileExists('infra/terraform/modules/oss-state/README.md')).toBe(true);
    const md = readRepoFile('infra/terraform/modules/oss-state/README.md');
    expect(md).toMatch(/OSS/);
    expect(md).toMatch(/TableStore/);
    expect(md).toMatch(/KMS/);
    expect(md).toMatch(/bootstrap/i);
  });

  test('1.3-UNIT-051: oss-state/outputs.tf exists; naming aligns with T0 bootstrap', () => {
    expect(repoFileExists('infra/terraform/modules/oss-state/outputs.tf')).toBe(true);
    const outs = readRepoFile('infra/terraform/modules/oss-state/outputs.tf');
    expect(outs).toMatch(/he-api-tfstate-/);
    expect(outs).toMatch(/terraform-lock/);
    expect(outs).toMatch(/LockID/);
  });
});

describe('AC1 / T5: envs/staging', () => {
  test('1.3-UNIT-060: envs/staging/main.tf imports vpc/ack/acr modules with relative source paths', () => {
    expect(stagingMain).toMatch(/module\s+"vpc"\s*\{[\s\S]+source\s*=\s*"\.\.\/\.\.\/modules\/vpc"/);
    expect(stagingMain).toMatch(/module\s+"ack"\s*\{[\s\S]+source\s*=\s*"\.\.\/\.\.\/modules\/ack"/);
    expect(stagingMain).toMatch(/module\s+"acr"\s*\{[\s\S]+source\s*=\s*"\.\.\/\.\.\/modules\/acr"/);
  });

  test('1.3-UNIT-061: envs/staging/main.tf passes worker_count=3, instance_type=ecs.c7.large, vpc_cidr=10.20.0.0/16, vswitches/AZs match', () => {
    const stagingVars = readRepoFile('infra/terraform/envs/staging/variables.tf');
    expect(stagingVars).toMatch(/variable\s+"worker_count"[\s\S]+default\s*=\s*3/);
    expect(stagingVars).toMatch(/variable\s+"worker_instance_type"[\s\S]+default\s*=\s*"ecs\.c7\.large"/);
    expect(stagingVars).toMatch(/variable\s+"vpc_cidr"[\s\S]+default\s*=\s*"10\.20\.0\.0\/16"/);
    expect(stagingVars).toMatch(/10\.20\.1\.0\/24/);
    expect(stagingVars).toMatch(/10\.20\.2\.0\/24/);
    expect(stagingVars).toMatch(/10\.20\.3\.0\/24/);
    expect(stagingVars).toMatch(/cn-shanghai-f/);
    expect(stagingVars).toMatch(/cn-shanghai-g/);
    expect(stagingVars).toMatch(/cn-shanghai-h/);
  });

  test('1.3-UNIT-062: envs/staging/terraform.tfvars.example contains api_server_public_access_allowed_cidrs with ≥2 example entries', () => {
    expect(stagingTfvarsExample).toMatch(/api_server_public_access_allowed_cidrs\s*=\s*\[/);
    // Count CIDR entries (commented-out lines do not count).
    const cidrMatches = stagingTfvarsExample
      .split(/api_server_public_access_allowed_cidrs\s*=\s*\[/)[1]
      ?.split(/\]/)[0]
      .split('\n')
      .filter((l) => /^\s*"[0-9.\/]+"/.test(l));
    expect(cidrMatches?.length || 0).toBeGreaterThanOrEqual(2);
  });

  test('1.3-UNIT-063: tfvars.example mentions GHA-runner refresh comment (api.github.com/meta + weekly refresh)', () => {
    expect(stagingTfvarsExample).toMatch(/api\.github\.com\/meta/);
    expect(stagingTfvarsExample).toMatch(/weekly\s+refresh/i);
  });

  test('1.3-UNIT-064: .gitignore appends infra/terraform/**/terraform.tfvars + .terraform/ + *.tfstate*', () => {
    expect(gitignoreTxt).toMatch(/infra\/terraform\/\*\*\/terraform\.tfvars/);
    expect(gitignoreTxt).toMatch(/infra\/terraform\/\*\*\/\.terraform\//);
    expect(gitignoreTxt).toMatch(/infra\/terraform\/\*\*\/\*\.tfstate/);
  });
});

describe('AC1 / T6: envs/prod (code-only)', () => {
  test('1.3-UNIT-070: envs/prod/main.tf worker_count=6, instance_type=ecs.c7.xlarge, vpc_cidr=10.30.0.0/16', () => {
    expect(prodMain).toMatch(/worker_count\s*=\s*6/);
    expect(prodMain).toMatch(/worker_instance_type\s*=\s*"ecs\.c7\.xlarge"/);
    const prodVars = readRepoFile('infra/terraform/envs/prod/variables.tf');
    expect(prodVars).toMatch(/variable\s+"vpc_cidr"[\s\S]+default\s*=\s*"10\.30\.0\.0\/16"/);
  });

  test('1.3-UNIT-071: envs/prod/main.tf vswitch_cidrs = 10.30.{1,2,3}.0/24 (NOT 10.20.x)', () => {
    const prodVars = readRepoFile('infra/terraform/envs/prod/variables.tf');
    expect(prodVars).toMatch(/10\.30\.1\.0\/24/);
    expect(prodVars).toMatch(/10\.30\.2\.0\/24/);
    expect(prodVars).toMatch(/10\.30\.3\.0\/24/);
    expect(prodVars).not.toMatch(/10\.20\.\d+\.0\/24/);
  });

  test('1.3-UNIT-072: envs/prod/main.tf ACK module call wrapped with lifecycle.prevent_destroy=true', () => {
    // Either the ACK module block itself OR a guard resource bound to module.ack must carry prevent_destroy=true.
    expect(prodMain).toMatch(
      /(?:module\s+"ack"|terraform_data\s+"ack_destroy_guard")[\s\S]{0,800}lifecycle\s*\{[\s\S]*?prevent_destroy\s*=\s*true/,
    );
    // Must reference module.ack.cluster_id (binds the guard's identity to the cluster).
    expect(prodMain).toMatch(/module\.ack\.cluster_id/);
  });

  test('1.3-UNIT-073: envs/prod/main.tf VPC module call wrapped with lifecycle.prevent_destroy=true', () => {
    expect(prodMain).toMatch(
      /(?:module\s+"vpc"|terraform_data\s+"vpc_destroy_guard")[\s\S]{0,800}lifecycle\s*\{[\s\S]*?prevent_destroy\s*=\s*true/,
    );
    expect(prodMain).toMatch(/module\.vpc\.vpc_id/);
  });

  test('1.3-UNIT-074: envs/prod/terraform.tfvars.example includes ACL placeholder + vpc_cidr=10.30.0.0/16', () => {
    expect(prodTfvarsExample).toMatch(/api_server_public_access_allowed_cidrs\s*=\s*\[/);
    expect(prodTfvarsExample).toMatch(/vpc_cidr\s*=\s*"10\.30\.0\.0\/16"/);
    expect(prodTfvarsExample).toMatch(/10\.30\.1\.0\/24/);
  });
});

describe('AC1 / T7: K8s base manifests', () => {
  const namespaceDocs = loadAllYaml('infra/k8s-base/namespace.yaml') as Array<
    | {
        kind?: string;
        metadata?: { name?: string; annotations?: Record<string, string> };
      }
    | null
  >;
  const rbacDocs = loadAllYaml('infra/k8s-base/rbac.yaml') as Array<
    | {
        kind?: string;
        metadata?: { name?: string; namespace?: string };
        rules?: Array<{ resources?: string[]; verbs?: string[] }>;
      }
    | null
  >;
  const npDocs = loadAllYaml('infra/k8s-base/network-policy.yaml') as Array<
    | { kind?: string; metadata?: { name?: string; namespace?: string } }
    | null
  >;
  const quotaDocs = loadAllYaml('infra/k8s-base/resource-quota.yaml') as Array<
    | {
        kind?: string;
        metadata?: { name?: string; namespace?: string };
        spec?: { hard?: Record<string, string> };
      }
    | null
  >;
  const kustomization = loadYaml<{ resources?: string[] }>('infra/k8s-base/kustomization.yaml');

  test('1.3-UNIT-080: namespace.yaml declares 6 namespaces (he-api-staging/he-api-prod/monitoring/cert-manager/ingress-nginx/argocd)', () => {
    const nsNames = namespaceDocs
      .filter((d): d is NonNullable<typeof d> => d?.kind === 'Namespace')
      .map((d) => d.metadata?.name);
    expect(nsNames).toEqual(
      expect.arrayContaining([
        'he-api-staging',
        'he-api-prod',
        'monitoring',
        'cert-manager',
        'ingress-nginx',
        'argocd',
      ]),
    );
    expect(nsNames.length).toBe(6);
  });

  test('1.3-UNIT-081: deferred 4 namespaces have annotations["managed-by"] = "story-1.4"', () => {
    const deferred = ['monitoring', 'cert-manager', 'ingress-nginx', 'argocd'];
    for (const name of deferred) {
      const doc = namespaceDocs.find(
        (d): d is NonNullable<typeof d> => d?.kind === 'Namespace' && d.metadata?.name === name,
      );
      expect(doc?.metadata?.annotations?.['managed-by']).toBe('story-1.4');
    }
  });

  test('1.3-UNIT-082: rbac.yaml declares ServiceAccount/default + Role/api-gateway-runtime + RoleBinding in he-api-staging AND he-api-prod', () => {
    for (const ns of ['he-api-staging', 'he-api-prod']) {
      const sa = rbacDocs.find(
        (d) => d?.kind === 'ServiceAccount' && d.metadata?.name === 'default' && d.metadata?.namespace === ns,
      );
      const role = rbacDocs.find(
        (d) => d?.kind === 'Role' && d.metadata?.name === 'api-gateway-runtime' && d.metadata?.namespace === ns,
      );
      const rb = rbacDocs.find(
        (d) => d?.kind === 'RoleBinding' && d.metadata?.namespace === ns,
      );
      expect(sa).toBeDefined();
      expect(role).toBeDefined();
      expect(rb).toBeDefined();
    }
  });

  test('1.3-UNIT-083: rbac.yaml Role rules include configmaps get/list/watch; do NOT include secrets in any rule', () => {
    const roles = rbacDocs.filter((d) => d?.kind === 'Role');
    expect(roles.length).toBeGreaterThan(0);
    for (const role of roles) {
      const cmRule = role?.rules?.find((r) => r.resources?.includes('configmaps'));
      expect(cmRule).toBeDefined();
      for (const verb of ['get', 'list', 'watch']) {
        expect(cmRule?.verbs).toContain(verb);
      }
      for (const rule of role?.rules || []) {
        expect(rule.resources).not.toContain('secrets');
      }
    }
  });

  test('1.3-UNIT-084: network-policy.yaml has default-deny-ingress + allow-from-ingress-nginx in each he-api-* ns', () => {
    for (const ns of ['he-api-staging', 'he-api-prod']) {
      const dd = npDocs.find(
        (d) => d?.kind === 'NetworkPolicy' && d.metadata?.name === 'default-deny-ingress' && d.metadata?.namespace === ns,
      );
      const allow = npDocs.find(
        (d) => d?.kind === 'NetworkPolicy' && d.metadata?.name === 'allow-from-ingress-nginx' && d.metadata?.namespace === ns,
      );
      expect(dd).toBeDefined();
      expect(allow).toBeDefined();
    }
  });

  test('1.3-UNIT-085: resource-quota.yaml he-api-staging quota: pods≤10, requests.cpu≤4, requests.memory≤8Gi', () => {
    const quota = quotaDocs.find(
      (d) => d?.kind === 'ResourceQuota' && d.metadata?.namespace === 'he-api-staging',
    );
    expect(quota).toBeDefined();
    expect(quota?.spec?.hard?.['pods']).toBe('10');
    expect(quota?.spec?.hard?.['requests.cpu']).toBe('4');
    expect(quota?.spec?.hard?.['requests.memory']).toBe('8Gi');
  });

  test('1.3-UNIT-086: kustomization.yaml resources contains namespace.yaml + rbac.yaml + network-policy.yaml + resource-quota.yaml', () => {
    expect(kustomization.resources).toEqual(
      expect.arrayContaining([
        'namespace.yaml',
        'rbac.yaml',
        'network-policy.yaml',
        'resource-quota.yaml',
      ]),
    );
  });
});

describe('AC1 / T9: .github/workflows/infra-lint.yml', () => {
  // GitHub Actions parses `on:` as a YAML key — but in JS YAML it is parsed
  // to boolean `true` because `on` is a YAML 1.1 boolean. Use raw text + regex.
  const infraLintParsed = loadYaml<Record<string, unknown>>(
    '.github/workflows/infra-lint.yml',
  );

  test('1.3-UNIT-090: infra-lint.yml on: pull_request branches=[main], paths includes infra/** and self', () => {
    // `on` is YAML 1.1 boolean — read via either `on` or `true` key.
    const onSpec =
      (infraLintParsed as { on?: unknown; true?: unknown }).on ??
      (infraLintParsed as { on?: unknown; true?: unknown }).true;
    const pr = (onSpec as { pull_request?: { branches?: string[]; paths?: string[] } })?.pull_request;
    expect(pr?.branches).toEqual(['main']);
    expect(pr?.paths).toEqual(expect.arrayContaining(['infra/**', '.github/workflows/infra-lint.yml']));
  });

  test('1.3-UNIT-091: terraform-validate has matrix env=[staging,prod]; uses hashicorp/setup-terraform@v3 with terraform_version 1.7+', () => {
    const jobs = (infraLintParsed as { jobs?: Record<string, unknown> }).jobs!;
    const tfv = jobs['terraform-validate'] as {
      strategy?: { matrix?: { env?: string[] } };
      steps?: Array<{ uses?: string; with?: Record<string, string> }>;
    };
    expect(tfv.strategy?.matrix?.env).toEqual(expect.arrayContaining(['staging', 'prod']));
    const setupTf = tfv.steps?.find((s) => s.uses?.startsWith('hashicorp/setup-terraform@v3'));
    expect(setupTf).toBeDefined();
    const tfVer = setupTf?.with?.['terraform_version'] || '';
    expect(/^1\.(7|8|9|1[0-9])(\.|$)/.test(tfVer)).toBe(true);
  });

  test('1.3-UNIT-092: terraform-validate steps: fmt -check + init -backend=false + validate + tflint --init + tflint --recursive', () => {
    expect(infraLintYmlText).toMatch(/terraform fmt -check -recursive/);
    expect(infraLintYmlText).toMatch(/terraform init -backend=false/);
    expect(infraLintYmlText).toMatch(/terraform validate/);
    expect(infraLintYmlText).toMatch(/tflint --init/);
    expect(infraLintYmlText).toMatch(/tflint --recursive/);
  });

  test('1.3-UNIT-093: helm-lint runs helm lint + helm template | kubeconform -strict -summary', () => {
    expect(infraLintYmlText).toMatch(/helm lint infra\/helm\/api-gateway\//);
    expect(infraLintYmlText).toMatch(/helm template[\s\S]+kubeconform[\s\S]+-strict[\s\S]+-summary/);
  });

  test('1.3-UNIT-094: k8s-manifest-validate runs kubeconform -strict -summary against infra/k8s-base/', () => {
    expect(infraLintYmlText).toMatch(
      /k8s-manifest-validate[\s\S]+kubeconform[\s\S]+infra\/k8s-base|infra\/k8s-base[\s\S]+kubeconform[\s\S]+-strict[\s\S]+-summary/,
    );
  });

  test('1.3-UNIT-095: workflow-level concurrency.group = infra-lint-${{ github.ref }}', () => {
    expect(infraLintYmlText).toMatch(/concurrency:\s*\n\s+group:\s*infra-lint-\$\{\{\s*github\.ref\s*\}\}/);
  });

  test('1.3-UNIT-096: concurrency.cancel-in-progress = ${{ github.event_name == "pull_request" }}', () => {
    expect(infraLintYmlText).toMatch(
      /cancel-in-progress:\s*\$\{\{\s*github\.event_name\s*==\s*'pull_request'\s*\}\}/,
    );
  });

  test('1.3-UNIT-097: repo-root .tflint.hcl pins plugin "alicloud" version=0.20.0 with explicit source', () => {
    expect(tflintHcl).toMatch(
      /plugin\s+"alicloud"\s*\{[\s\S]+version\s*=\s*"0\.20\.0"[\s\S]+source\s*=\s*"github\.com\/terraform-linters\/tflint-ruleset-alicloud"/,
    );
  });

  test('1.3-UNIT-098: README "Branch protection 推荐配置" lists all 4 new check names verbatim', () => {
    const idx = readmeMd.indexOf('Branch protection 推荐配置');
    expect(idx).toBeGreaterThan(0);
    // The 4 new check names MUST appear within ~2 KiB of the heading.
    const section = readmeMd.slice(idx, idx + 2000);
    for (const name of [
      'terraform-validate (staging)',
      'terraform-validate (prod)',
      'helm-lint',
      'k8s-manifest-validate',
    ]) {
      expect(section).toContain(name);
    }
  });
});

describe('AC1 / T10: README "Infrastructure" section', () => {
  test('1.3-UNIT-110: README contains "Infrastructure" H1/H2 section heading', () => {
    expect(readmeMd).toMatch(/^#{1,2}\s+Infrastructure\b/m);
  });

  test('1.3-UNIT-111: README "Apply 顺序" subsection lists 4 ordered steps (bootstrap → terraform apply → kubectl apply -k → helm install)', () => {
    expect(readmeMd).toMatch(/Apply 顺序/);
    const section = readmeMd.split(/Apply 顺序/)[1]?.slice(0, 2500) || '';
    expect(section).toMatch(/bootstrap-state-backend\.sh/);
    expect(section).toMatch(/terraform\s+(init|plan|apply)/);
    expect(section).toMatch(/kubectl apply -k\s+infra\/k8s-base\//);
    expect(section).toMatch(/helm install\s+api-gateway/);
  });

  test('1.3-UNIT-112: README "RAM 权限" lists 6 policies (ECS/VPC/CS/CR/OSS + custom KMS+TableStore) AND "no Admin policy"', () => {
    const section = readmeMd.split(/必备阿里云 RAM 权限/)[1]?.slice(0, 2500) || '';
    expect(section).toMatch(/AliyunECSFullAccess/);
    expect(section).toMatch(/AliyunVPCFullAccess/);
    expect(section).toMatch(/AliyunCSFullAccess/);
    expect(section).toMatch(/AliyunCRFullAccess/);
    expect(section).toMatch(/AliyunOSSFullAccess/);
    expect(section).toMatch(/KMS/);
    expect(section).toMatch(/TableStore|OTS/i);
    expect(section).toMatch(/No Admin policy|不使用 Admin/i);
  });

  test('1.3-UNIT-113: README "Cost estimate" mentions ¥800/月 baseline AND ¥1500/月 escalation gate', () => {
    const section = readmeMd.split(/Cost estimate/)[1]?.slice(0, 2000) || '';
    expect(section).toMatch(/¥800/);
    expect(section).toMatch(/¥1500/);
  });

  test('1.3-UNIT-114: README "Story 1.2 dependency 闭环对照表" contains 5 rows (ACR_REGISTRY/USERNAME/PASSWORD + values-staging.yaml + namespace)', () => {
    const section = readmeMd.split(/Story 1\.2 dependency 闭环对照表/)[1]?.slice(0, 3000) || '';
    expect(section).toMatch(/ACR_REGISTRY/);
    expect(section).toMatch(/ACR_USERNAME/);
    expect(section).toMatch(/ACR_PASSWORD/);
    expect(section).toMatch(/values-staging\.yaml/);
    expect(section).toMatch(/he-api-staging/);
  });

  test('1.3-UNIT-115: README required-checks list contains all 10 names (6 from 1.2 + 4 from 1.3)', () => {
    const required = [
      'lint-ts',
      'lint-go',
      'unit-ts',
      'unit-go',
      'integration',
      'build-image-pr',
      'terraform-validate (staging)',
      'terraform-validate (prod)',
      'helm-lint',
      'k8s-manifest-validate',
    ];
    for (const name of required) {
      expect(readmeMd).toContain(name);
    }
  });

  test('1.3-UNIT-116: README rollback section contains "staging 集群默认保留至 Story 1.4" AND "必保留资源" with ACR EE / OSS / KMS / TableStore', () => {
    expect(readmeMd).toMatch(/staging 集群默认保留至 Story 1\.4/);
    const section = readmeMd.split(/必保留资源/)[1]?.slice(0, 2000) || '';
    expect(section).toMatch(/ACR Enterprise Basic|ACR EE/i);
    expect(section).toMatch(/OSS state bucket|OSS state/i);
    expect(section).toMatch(/KMS/);
    expect(section).toMatch(/TableStore/);
  });

  test('1.3-UNIT-117: README references scripts/infra/bootstrap-state-backend.sh literally', () => {
    expect(readmeMd).toMatch(/scripts\/infra\/bootstrap-state-backend\.sh/);
  });
});

// ============================================================
// AC2: Helm chart 可部署
// ============================================================

describe('AC2 / T8: Helm chart (infra/helm/api-gateway/)', () => {
  const chartYaml = loadYaml<{
    apiVersion?: string;
    name?: string;
    type?: string;
    version?: string;
    appVersion?: string;
    kubeVersion?: string;
  }>('infra/helm/api-gateway/Chart.yaml');
  const valuesYaml = loadYaml<Record<string, unknown>>('infra/helm/api-gateway/values.yaml');
  const valuesStagingYaml = loadYaml<Record<string, unknown>>(
    'infra/helm/api-gateway/values-staging.yaml',
  );
  const deploymentText = readRepoFile('infra/helm/api-gateway/templates/deployment.yaml');
  const serviceText = readRepoFile('infra/helm/api-gateway/templates/service.yaml');
  const npText = readRepoFile('infra/helm/api-gateway/templates/networkpolicy.yaml');
  const saText = readRepoFile('infra/helm/api-gateway/templates/serviceaccount.yaml');
  const notesText = readRepoFile('infra/helm/api-gateway/templates/NOTES.txt');

  test('1.3-UNIT-200: Chart.yaml apiVersion=v2, name=api-gateway, type=application, version=0.1.0, kubeVersion>=1.29.0-0', () => {
    expect(chartYaml.apiVersion).toBe('v2');
    expect(chartYaml.name).toBe('api-gateway');
    expect(chartYaml.type).toBe('application');
    expect(chartYaml.version).toBe('0.1.0');
    expect(chartYaml.kubeVersion).toBe('>=1.29.0-0');
  });

  test('1.3-UNIT-201: values.yaml contains all default sections (image / replicaCount / service / resources / probes / ingress / networkPolicy / serviceAccount)', () => {
    for (const key of [
      'image',
      'replicaCount',
      'service',
      'resources',
      'probes',
      'ingress',
      'networkPolicy',
      'serviceAccount',
    ]) {
      expect(valuesYaml).toHaveProperty(key);
    }
  });

  test('1.3-UNIT-202: values-staging.yaml top-level keys are EXACTLY a subset of {image, service, networkPolicy, serviceAccount}', () => {
    const whitelist = new Set(['image', 'service', 'networkPolicy', 'serviceAccount']);
    const keys = Object.keys(valuesStagingYaml);
    for (const k of keys) {
      expect(whitelist.has(k)).toBe(true);
    }
  });

  test('1.3-UNIT-203: values-staging.yaml does NOT contain `ingress` key at any depth', () => {
    const json = JSON.stringify(valuesStagingYaml);
    expect(json).not.toMatch(/"ingress"\s*:/);
  });

  test('1.3-UNIT-204: values-staging.yaml does NOT contain `autoscaling` key at any depth', () => {
    const json = JSON.stringify(valuesStagingYaml);
    expect(json).not.toMatch(/"autoscaling"\s*:/);
  });

  test('1.3-UNIT-205: values-staging.yaml does NOT contain `resources.requests` or `resources.limits` overrides', () => {
    const resources = (valuesStagingYaml as { resources?: { requests?: unknown; limits?: unknown } })
      .resources;
    expect(resources).toBeUndefined();
  });

  test('1.3-UNIT-206: values-staging.yaml does NOT contain `env` or `envFrom` (no plaintext credentials)', () => {
    const json = JSON.stringify(valuesStagingYaml);
    expect(json).not.toMatch(/"env"\s*:/);
    expect(json).not.toMatch(/"envFrom"\s*:/);
  });

  test('1.3-UNIT-207: values-staging.yaml image.repository = "${ACR_REGISTRY}/he-api/api-gateway"', () => {
    const image = (valuesStagingYaml as { image?: { repository?: string } }).image;
    expect(image?.repository).toBe('${ACR_REGISTRY}/he-api/api-gateway');
  });

  test('1.3-UNIT-208: values-staging.yaml image.tag = "0.1.0" (placeholder for git SHA rewrite)', () => {
    const image = (valuesStagingYaml as { image?: { tag?: string } }).image;
    expect(image?.tag).toBe('0.1.0');
  });

  test('1.3-UNIT-209: values-staging.yaml image.pullPolicy = "IfNotPresent"', () => {
    const image = (valuesStagingYaml as { image?: { pullPolicy?: string } }).image;
    expect(image?.pullPolicy).toBe('IfNotPresent');
  });

  test('1.3-UNIT-210: values-staging.yaml service.type=ClusterIP, service.port=8080', () => {
    const service = (valuesStagingYaml as { service?: { type?: string; port?: number } }).service;
    expect(service?.type).toBe('ClusterIP');
    expect(service?.port).toBe(8080);
  });

  test('1.3-UNIT-211: values-staging.yaml networkPolicy.enabled = true (explicit override)', () => {
    const np = (valuesStagingYaml as { networkPolicy?: { enabled?: boolean } }).networkPolicy;
    expect(np?.enabled).toBe(true);
  });

  test('1.3-UNIT-212: values-staging.yaml serviceAccount.create=true AND serviceAccount.name=""', () => {
    const sa = (valuesStagingYaml as { serviceAccount?: { create?: boolean; name?: string } })
      .serviceAccount;
    expect(sa?.create).toBe(true);
    expect(sa?.name).toBe('');
  });

  test('1.3-UNIT-213: deployment.yaml references {{ .Values.image.repository }} AND {{ .Values.image.tag }}', () => {
    expect(deploymentText).toMatch(/\{\{\s*\.Values\.image\.repository\s*\}\}/);
    expect(deploymentText).toMatch(/\{\{\s*\.Values\.image\.tag\s*\}\}/);
  });

  test('1.3-UNIT-214: deployment.yaml readinessProbe httpGet path=/healthz port=8080', () => {
    expect(deploymentText).toMatch(/readinessProbe:[\s\S]+httpGet:[\s\S]+path:\s*\{\{\s*\.Values\.probes\.readiness\.httpGet\.path\s*\}\}/);
    // values.yaml resolves the path/port — verify the default values.
    const probes = (valuesYaml as { probes?: { readiness?: { httpGet?: { path?: string; port?: number } } } })
      .probes;
    expect(probes?.readiness?.httpGet?.path).toBe('/healthz');
    expect(probes?.readiness?.httpGet?.port).toBe(8080);
  });

  test('1.3-UNIT-215: deployment.yaml livenessProbe httpGet path=/healthz port=8080', () => {
    expect(deploymentText).toMatch(/livenessProbe:[\s\S]+httpGet:[\s\S]+path:\s*\{\{\s*\.Values\.probes\.liveness\.httpGet\.path\s*\}\}/);
    const probes = (valuesYaml as { probes?: { liveness?: { httpGet?: { path?: string; port?: number } } } })
      .probes;
    expect(probes?.liveness?.httpGet?.path).toBe('/healthz');
    expect(probes?.liveness?.httpGet?.port).toBe(8080);
  });

  test('1.3-UNIT-216: deployment.yaml securityContext.runAsNonRoot = true', () => {
    expect(deploymentText).toMatch(/runAsNonRoot:\s*\{\{\s*\.Values\.securityContext\.runAsNonRoot/);
    expect(
      (valuesYaml as { securityContext?: { runAsNonRoot?: boolean } }).securityContext?.runAsNonRoot,
    ).toBe(true);
  });

  test('1.3-UNIT-217: deployment.yaml securityContext.runAsUser = 65532 (distroless nonroot)', () => {
    expect(deploymentText).toMatch(/runAsUser:\s*\{\{\s*\.Values\.securityContext\.runAsUser/);
    expect(
      (valuesYaml as { securityContext?: { runAsUser?: number } }).securityContext?.runAsUser,
    ).toBe(65532);
  });

  test('1.3-UNIT-218: deployment.yaml securityContext.readOnlyRootFilesystem = true', () => {
    expect(deploymentText).toMatch(
      /readOnlyRootFilesystem:\s*\{\{\s*\.Values\.securityContext\.readOnlyRootFilesystem/,
    );
    expect(
      (valuesYaml as { securityContext?: { readOnlyRootFilesystem?: boolean } }).securityContext
        ?.readOnlyRootFilesystem,
    ).toBe(true);
  });

  test('1.3-UNIT-219: deployment.yaml securityContext.allowPrivilegeEscalation = false', () => {
    expect(deploymentText).toMatch(
      /allowPrivilegeEscalation:\s*\{\{\s*\.Values\.securityContext\.allowPrivilegeEscalation/,
    );
    expect(
      (valuesYaml as { securityContext?: { allowPrivilegeEscalation?: boolean } }).securityContext
        ?.allowPrivilegeEscalation,
    ).toBe(false);
  });

  test('1.3-UNIT-220: deployment.yaml securityContext.capabilities.drop = ["ALL"]', () => {
    expect(deploymentText).toMatch(/capabilities:[\s\S]+drop:/);
    const drop = (
      valuesYaml as { securityContext?: { capabilities?: { drop?: string[] } } }
    ).securityContext?.capabilities?.drop;
    expect(drop).toEqual(['ALL']);
  });

  test('1.3-UNIT-221: deployment.yaml securityContext.seccompProfile.type = "RuntimeDefault"', () => {
    expect(deploymentText).toMatch(/seccompProfile:[\s\S]+type:\s*\{\{\s*\.Values\.securityContext\.seccompProfile\.type/);
    expect(
      (valuesYaml as { securityContext?: { seccompProfile?: { type?: string } } }).securityContext
        ?.seccompProfile?.type,
    ).toBe('RuntimeDefault');
  });

  test('1.3-UNIT-222: deployment.yaml serviceAccountName references .Values.serviceAccount', () => {
    expect(deploymentText).toMatch(/serviceAccountName:\s*\{\{[\s\S]+serviceAccount/);
  });

  test('1.3-UNIT-223: service.yaml type=ClusterIP, port=8080 (helm-templated)', () => {
    expect(serviceText).toMatch(/type:\s*\{\{\s*\.Values\.service\.type\s*\}\}/);
    expect(serviceText).toMatch(/port:\s*\{\{\s*\.Values\.service\.port\s*\}\}/);
    expect((valuesYaml as { service?: { type?: string; port?: number } }).service?.type).toBe(
      'ClusterIP',
    );
    expect((valuesYaml as { service?: { type?: string; port?: number } }).service?.port).toBe(8080);
  });

  test('1.3-UNIT-224: networkpolicy.yaml wrapped in if .Values.networkPolicy.enabled; default-deny + allow-from kube-system/ingress-nginx', () => {
    expect(npText).toMatch(/\{\{-?\s*if\s+\.Values\.networkPolicy\.enabled\s*\}\}/);
    expect(npText).toMatch(/default-deny/);
    expect(npText).toMatch(/allow-from-ingress-nginx/);
    expect(npText).toMatch(/namespaceSelector:[\s\S]+ingress-nginx/);
  });

  test('1.3-UNIT-225: serviceaccount.yaml wrapped in if .Values.serviceAccount.create', () => {
    expect(saText).toMatch(/\{\{-?\s*if\s+\.Values\.serviceAccount\.create\s*\}\}/);
  });

  test('1.3-UNIT-226: NOTES.txt mentions kubectl rollout status AND ImagePullBackOff expected at this stage', () => {
    expect(notesText).toMatch(/kubectl rollout status/);
    expect(notesText).toMatch(/ImagePullBackOff/);
  });

  test('1.3-UNIT-227: .helmignore exists with standard ignores (.git/, *.tmproj)', () => {
    expect(repoFileExists('infra/helm/api-gateway/.helmignore')).toBe(true);
    const helmignore = readRepoFile('infra/helm/api-gateway/.helmignore');
    expect(helmignore).toMatch(/\.git\//);
    expect(helmignore).toMatch(/\*\.tmproj/);
  });
});

// ============================================================
// Integration scenarios (PR-driven; evidence in dev-log T11)
// ============================================================

describe('Integration (PR-driven, evidence in docs/dev/logs/1.3-dev-log.md)', () => {
  test.skip('1.3-INT-001: PR touching infra/** triggers 4 new checks all green (terraform-validate ×2 / helm-lint / k8s-manifest-validate)', () => {
    // Evidence: Operator Hand-off §6.5 in docs/dev/logs/1.3-dev-log.md
  });
  test.skip('1.3-INT-002: PR with terraform fmt violation → terraform-validate red', () => {});
  test.skip('1.3-INT-003: PR with Helm template syntax error → helm-lint red', () => {});
  test.skip('1.3-INT-004: PR with malformed K8s manifest → k8s-manifest-validate red', () => {});
  test.skip('1.3-INT-005: Story 1.2 closure regression — probe_helm.outputs.exists=true → real yq write → push → no double-trigger within 5 min', () => {
    // Evidence: Operator Hand-off §6.5 (L-5 gate)
  });
  test.skip('1.3-INT-006: Concurrent PR pushes on same ref → second infra-lint cancels first', () => {});
});

// ============================================================
// E2E scenarios (single-shot Dev terraform apply on staging)
// ============================================================

describe('E2E (single-shot Dev apply on staging, evidence in dev-log T11/T12)', () => {
  test.skip('1.3-E2E-001: terraform apply staging exits 0; terraform output + Aliyun console screenshot in dev-log', () => {});
  test.skip('1.3-E2E-002: kubectl get nodes shows 3 Ready across 3 AZs within 30s', () => {});
  test.skip('1.3-E2E-003: kubectl auth can-i get secrets as default SA → "no"', () => {});
  test.skip('1.3-E2E-004: kubectl get ns returns 6 he-api-related namespaces', () => {});
  test.skip('1.3-E2E-005: kubectl get networkpolicy -n he-api-staging returns default-deny + allow-from-ingress-nginx', () => {});
  test.skip('1.3-E2E-006: nmap -p 6443 from non-whitelisted IP → filtered/timeout (Path A2 ACL)', () => {});
  test.skip('1.3-E2E-007: cluster + Epic-shared resources retained; dev-log records retention_status=RETAINED_FOR_STORY_1_4', () => {});
  test.skip('1.3-E2E-008: helm lint exits 0 with no ERROR-level warnings', () => {});
  test.skip('1.3-E2E-009: helm template | kubeconform -strict -summary exits 0', () => {});
  test.skip('1.3-E2E-010: helm install --dry-run=server exits 0 (real K8s admission)', () => {});
  test.skip('1.3-E2E-011: real helm install completes; pod enters ImagePullBackOff', () => {});
  test.skip('1.3-E2E-012: kubectl describe pod events show "Failed to pull image ... not found" (NOT RBAC/NetworkPolicy denial)', () => {});
  test.skip('1.3-E2E-013: helm uninstall succeeds; no leftover api-gateway pod', () => {});
});

// ============================================================
// Blind-Spot scenarios
// ============================================================

describe('[BLIND-SPOT] Boundary conditions', () => {
  test.skip('[BLIND-SPOT] 1.3-BLIND-BOUNDARY-001: empty allowed_cidrs when enabled=true → terraform plan fails on precondition', () => {});
  test.skip('[BLIND-SPOT] 1.3-BLIND-BOUNDARY-002: VPC CIDR overlapping pod CIDR → terraform validate/apply fails', () => {});

  test('[BLIND-SPOT] 1.3-BLIND-BOUNDARY-003: adding 9th key to values-staging.yaml makes 1.3-UNIT-202 fail (whitelist enforcement)', () => {
    // Counter-test: inject an extra unknown key into an in-memory copy and
    // confirm the whitelist guard rejects it.
    const valuesStagingText = readRepoFile('infra/helm/api-gateway/values-staging.yaml');
    const mutated = parseYaml(valuesStagingText + '\nautoscaling:\n  enabled: false\n') as Record<string, unknown>;
    const whitelist = new Set(['image', 'service', 'networkPolicy', 'serviceAccount']);
    const allInWhitelist = Object.keys(mutated).every((k) => whitelist.has(k));
    expect(allInWhitelist).toBe(false);
  });

  test.skip('[BLIND-SPOT] 1.3-BLIND-BOUNDARY-004: instance_type="Personal" on alicloud_cr_ee_instance → terraform plan fails (schema)', () => {});
  test.skip('[BLIND-SPOT] 1.3-BLIND-BOUNDARY-005: two vSwitches with identical CIDR → terraform validate/apply fails', () => {});
});

describe('[BLIND-SPOT] Error handling', () => {
  test.skip('[BLIND-SPOT] 1.3-BLIND-ERROR-001: KMS key disabled mid-apply → terraform apply fails on first state-backend write', () => {});
  test.skip('[BLIND-SPOT] 1.3-BLIND-ERROR-002: TableStore endpoint unreachable → terraform plan/apply fails on lock acquisition', () => {});
  test.skip('[BLIND-SPOT] 1.3-BLIND-ERROR-003: RAM missing AliyunCSFullAccess → terraform apply fails with explicit Forbidden', () => {});
  test.skip('[BLIND-SPOT] 1.3-BLIND-ERROR-004: Helm template references undefined .Values.foo → helm lint exits non-0', () => {});
});

describe('[BLIND-SPOT] Flow / Concurrency', () => {
  test.skip('[BLIND-SPOT] 1.3-BLIND-FLOW-001: two concurrent terraform applies → second blocks on TableStore lock; no state corruption', () => {});
  test.skip('[BLIND-SPOT] 1.3-BLIND-FLOW-002: terraform apply Ctrl-C mid-resource → terraform force-unlock works; subsequent plan succeeds', () => {});
  test.skip('[BLIND-SPOT] 1.3-BLIND-CONCURRENCY-001: two helm install api-gateway → second fails "release already exists"', () => {});
});

describe('[BLIND-SPOT] Data integrity', () => {
  test.skip('[BLIND-SPOT] 1.3-BLIND-DATA-001: 3-way ACR endpoint consistency (terraform output ↔ GitHub secret ↔ helm values prefix)', () => {});
  test.skip('[BLIND-SPOT] 1.3-BLIND-DATA-002: kubeconfig output has sensitive=true; never written to plain file via terraform output -raw', () => {});
  test.skip('[BLIND-SPOT] 1.3-BLIND-DATA-003: OSS state bucket policy denies any principal except tfstate-operator', () => {});
});

describe('[BLIND-SPOT] Resource cleanup', () => {
  test.skip('[BLIND-SPOT] 1.3-BLIND-RESOURCE-001: TableStore terraform-lock has no rows for current LockID after apply (lock released)', () => {});

  test('[BLIND-SPOT] 1.3-BLIND-RESOURCE-002: Epic-shared resources (ACR EE / OSS / KMS / TableStore) explicitly excluded from any destroy script', () => {
    // Static: README rollback section MUST mention all 4 Epic-shared resources as "必保留资源" (excluded from destroy).
    const idx = readmeMd.indexOf('必保留资源');
    expect(idx).toBeGreaterThan(0);
    const section = readmeMd.slice(idx, idx + 2500);
    for (const term of ['ACR Enterprise Basic', 'OSS state', 'KMS', 'TableStore']) {
      expect(section).toContain(term);
    }
  });
});
